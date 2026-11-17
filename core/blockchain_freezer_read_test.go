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

package core

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/params"
)

func TestHasBlockAndStateDuringFreeze(t *testing.T) {
	kv, err := pebble.New(t.TempDir(), 16, 16, "", false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := rawdb.NewDatabaseWithFreezer(kv, t.TempDir(), "", false)
	if err != nil {
		kv.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	chain, err := NewBlockChain(db, &Genesis{Config: params.AllEthashProtocolChanges}, ethash.NewFaker(), DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chain.Stop)
	genesis := chain.Genesis()
	if !chain.HasBlockAndState(genesis.Hash(), 0) {
		t.Fatal("genesis block and state are missing")
	}
	held, release := make(chan struct{}), make(chan struct{})
	freezeDone := make(chan error, 1)
	go func() {
		_, err := db.ModifyAncients(func(ethdb.AncientWriteOp) error {
			close(held)
			<-release
			return nil
		})
		freezeDone <- err
	}()
	select {
	case <-held:
	case err := <-freezeDone:
		t.Fatalf("freezer writer did not acquire its lock: %v", err)
	}
	result := make(chan bool, 1)
	queryDone := make(chan struct{})
	go func() {
		result <- chain.HasBlockAndState(common.Hash{0xff}, 1)
		close(queryDone)
	}()
	defer func() {
		close(release)
		if err := <-freezeDone; err != nil {
			t.Error(err)
		}
		<-queryDone
	}()
	select {
	case found := <-result:
		if found {
			t.Fatal("unknown block reported as present")
		}
	case <-time.After(time.Second):
		t.Fatal("unknown block lookup waited for the freezer writer")
	}
}
