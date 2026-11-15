// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package snapshot

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
)

type blockedSnapshotStore struct {
	ethdb.KeyValueStore
	reads   atomic.Int32
	entered chan struct{}
	release <-chan struct{}
}

func (db *blockedSnapshotStore) Get(key []byte) ([]byte, error) {
	db.reads.Add(1)
	db.entered <- struct{}{}
	<-db.release
	return db.KeyValueStore.Get(key)
}

func newBlockedDiskLayer(t *testing.T) (*diskLayer, *blockedSnapshotStore, func()) {
	t.Helper()
	db, err := pebble.New(t.TempDir(), 16, 16, "", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	gate := make(chan struct{})
	store := &blockedSnapshotStore{KeyValueStore: db, entered: make(chan struct{}, 32), release: gate}
	layer := &diskLayer{diskdb: store, cache: fastcache.New(1024 * 500)}
	t.Cleanup(func() { layer.Release() })
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(unblock)
	return layer, store, unblock
}

func TestDiskStorageConcurrentReads(t *testing.T) {
	for _, present := range []bool{true, false} {
		name := "missing"
		if present {
			name = "present"
		}
		t.Run(name, func(t *testing.T) {
			layer, store, unblock := newBlockedDiskLayer(t)
			account, slot := common.Hash{1}, common.Hash{2}
			var want []byte
			if present {
				want = []byte{0x81, 0x80}
				rawdb.WriteStorageSnapshot(store, account, slot, want)
			}
			const readers = 16
			var wg sync.WaitGroup
			results := make([][]byte, readers)
			errs := make([]error, readers)
			start := make(chan struct{})
			for i := range readers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results[i], errs[i] = layer.Storage(account, slot)
				}()
			}
			close(start)
			<-store.entered
			time.Sleep(50 * time.Millisecond)
			unblock()
			wg.Wait()
			if n := store.reads.Load(); n != 1 {
				t.Fatalf("concurrent cold reads reached the database %d times, want 1", n)
			}
			for i := range readers {
				if errs[i] != nil || !bytes.Equal(results[i], want) {
					t.Fatalf("reader %d: value %x, error %v; want %x", i, results[i], errs[i], want)
				}
			}
			if present {
				results[0][0] ^= 0xff
				for i := 1; i < readers; i++ {
					if !bytes.Equal(results[i], want) {
						t.Fatalf("reader %d shares mutable storage bytes", i)
					}
				}
			}
			cached, err := layer.Storage(account, slot)
			if err != nil || !bytes.Equal(cached, want) || store.reads.Load() != 1 {
				t.Fatalf("cached value %x, error %v, reads %d", cached, err, store.reads.Load())
			}
		})
	}
}

func TestDiskStorageDifferentKeysParallel(t *testing.T) {
	layer, store, unblock := newBlockedDiskLayer(t)
	account := common.Hash{1}
	var wg sync.WaitGroup
	for _, slot := range []common.Hash{{2}, {3}} {
		rawdb.WriteStorageSnapshot(store, account, slot, []byte{0x80})
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := layer.Storage(account, slot); err != nil {
				t.Error(err)
			}
		}()
	}
	for range 2 {
		select {
		case <-store.entered:
		case <-time.After(time.Second):
			unblock()
			wg.Wait()
			t.Fatal("different storage keys blocked each other's database reads")
		}
	}
	unblock()
	wg.Wait()
}
