// Copyright 2019 The go-ethereum Authors
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

package pebble

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/dbtest"
)

func TestPebbleDB(t *testing.T) {
	t.Run("DatabaseSuite", func(t *testing.T) {
		dbtest.TestDatabaseSuite(t, func() ethdb.KeyValueStore {
			db, err := pebble.Open("", &pebble.Options{
				FS: vfs.NewMem(),
			})
			if err != nil {
				t.Fatal(err)
			}
			return &Database{
				db: db,
			}
		})
	})
}

func BenchmarkPebbleDB(b *testing.B) {
	dbtest.BenchDatabaseSuite(b, func() ethdb.KeyValueStore {
		db, err := pebble.Open("", &pebble.Options{
			FS: vfs.NewMem(),
		})
		if err != nil {
			b.Fatal(err)
		}
		return &Database{
			db: db,
		}
	})
}

func TestPebbleLogData(t *testing.T) {
	db, err := pebble.Open("", &pebble.Options{
		FS: vfs.NewMem(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = db.Get(nil)
	if !errors.Is(err, pebble.ErrNotFound) {
		t.Fatal("Unknown database entry")
	}

	b := db.NewBatch()
	b.LogData(nil, nil)
	db.Apply(b, pebble.Sync)

	_, _, err = db.Get(nil)
	if !errors.Is(err, pebble.ErrNotFound) {
		t.Fatal("Unknown database entry")
	}
}

func TestPebbleCompactionAndMemTableLimits(t *testing.T) {
	if got := pebbleMemTableSize(40960); got != 256*1024*1024 {
		t.Fatalf("pebbleMemTableSize(40960) = %d, want %d", got, 256*1024*1024)
	}
	if got := pebbleMemTableSize(512); got != 512*1024*1024/2/6 {
		t.Fatalf("pebbleMemTableSize(512) = %d, want %d", got, 512*1024*1024/2/6)
	}
	if got := pebbleMaxConcurrentCompactions(); got < 1 || got > 2 {
		t.Fatalf("pebbleMaxConcurrentCompactions() = %d, want in [1, 2]", got)
	}

	dir := t.TempDir()
	db, err := New(dir, 512, 64, "test/", false)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer db.Close()

	matches, err := filepath.Glob(filepath.Join(dir, "OPTIONS-*"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("expected OPTIONS file in %s, err=%v", dir, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read OPTIONS failed: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "max_concurrent_compactions=2") && !strings.Contains(text, "max_concurrent_compactions=1") {
		t.Fatalf("unexpected max_concurrent_compactions in OPTIONS:\n%s", text)
	}
}
