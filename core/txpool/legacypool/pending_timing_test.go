package legacypool

import (
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

func TestPendingTimingMeasuresActualPoolMutexWait(t *testing.T) {
	pool, key := setupPoolWithConfig(params.TestChainConfig)
	t.Cleanup(func() { pool.Close() })
	pool.currentState.AddBalance(crypto.PubkeyToAddress(key.PublicKey), uint256.NewInt(1000000000000000000), tracing.BalanceChangeUnspecified)
	var tx *types.Transaction
	for price := int64(1); ; price++ {
		tx = pricedTransaction(0, 21000, big.NewInt(price), key)
		if pendingtrace.Sampled(tx.Hash()) {
			break
		}
	}
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	done := make(chan []error, 1)
	func() {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		go func() { done <- pool.Add([]*types.Transaction{tx}, false) }()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
				if e.Hash == tx.Hash() && e.Stage == "legacy_add_begin" {
					time.Sleep(30 * time.Millisecond)
					return
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("Add did not start while the pool mutex was held")
	}()
	select {
	case errs := <-done:
		if len(errs) != 1 || errs[0] != nil {
			t.Fatalf("valid transaction outcome changed: %v", errs)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Add remained blocked after mutex release")
	}
	stages := make(map[string]pendingtrace.Event)
	for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
		if e.Hash == tx.Hash() {
			stages[e.Stage] = e
		}
	}
	wait, acquired, released := stages["legacy_lock_wait"], stages["legacy_lock_acquired"], stages["legacy_lock_released"]
	if acquired.MonotonicUS-wait.MonotonicUS < 25000 || released.MonotonicUS < acquired.MonotonicUS || wait.Operation != acquired.Operation || acquired.Operation != released.Operation {
		t.Fatalf("pool contention merged with validation or hold time: %+v", stages)
	}
	if stages["legacy_promote_begin"].Sequence == 0 || stages["legacy_promote_scheduled"].Sequence == 0 {
		t.Fatal("promotion scheduler wait is not independently observable")
	}
}
