package legacypool

import (
	"encoding/binary"
	"fmt"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func BenchmarkTruncatePendingBelowLimit(b *testing.B) {
	for _, size := range []int{38_874, 80_000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			pool := &LegacyPool{config: DefaultConfig, pending: make(map[common.Address]*list), all: newLookup()}
			pool.config.GlobalSlots = 150000
			for i := 0; i < size; i++ {
				var addr common.Address
				binary.BigEndian.PutUint64(addr[12:], uint64(i+1))
				tx := types.NewTx(&types.LegacyTx{To: &addr, Gas: 21000})
				l := newList(true)
				l.Add(tx, 10)
				pool.pending[addr] = l
				pool.all.Add(tx)
			}
			b.ReportAllocs()
			for b.Loop() {
				pool.truncatePending()
			}
			if pool.all.Count() != size {
				b.Fatal("unexpected eviction below limit")
			}
		})
	}
}

func TestTruncatePendingWithQueuedUpperBound(t *testing.T) {
	for _, count := range []int{3, 4, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			pool, key := setupPoolWithConfig(params.TestChainConfig)
			defer pool.Close()
			pool.mu.Lock()
			defer pool.mu.Unlock()
			pool.config.GlobalSlots = 4
			pool.config.AccountSlots = 1
			addr := crypto.PubkeyToAddress(key.PublicKey)
			if err := pool.reserver.Hold(addr); err != nil {
				t.Fatal(err)
			}
			pool.currentState.SetBalance(addr, uint256.NewInt(1000000000000000000), tracing.BalanceChangeUnspecified)
			for nonce := 0; nonce < count; nonce++ {
				tx := transaction(uint64(nonce), 100000, key)
				if _, err := pool.enqueueTx(tx.Hash(), tx, true); err != nil {
					t.Fatal(err)
				}
			}
			queued := transaction(10, 100000, key)
			if _, err := pool.enqueueTx(queued.Hash(), queued, true); err != nil {
				t.Fatal(err)
			}
			pool.promoteExecutables([]common.Address{addr})
			pool.truncatePending()
			want := min(count, 4)
			if pool.pending[addr].Len() != want || pool.queue.stats() != 1 || pool.all.Count() != want+1 {
				t.Fatal("pending/queued counts changed incorrectly")
			}
			if pool.pendingNonces.get(addr) != uint64(want) {
				t.Fatal("truncation left the wrong next nonce")
			}
		})
	}
}
