package legacypool

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"math/big"
	"testing"
	"time"
)

func TestArbHashLookupDoesNotWaitForResetLockAndDropsReplacement(t *testing.T) {
	pool := &LegacyPool{all: newLookup()}
	old := types.NewTransaction(1, common.Address{1}, big.NewInt(0), 21000, big.NewInt(1), nil)
	next := types.NewTransaction(1, common.Address{1}, big.NewInt(0), 21000, big.NewInt(2), nil)
	pool.all.Add(old)
	pool.mu.Lock()
	done := make(chan *types.Transaction, 1)
	go func() { done <- pool.Get(old.Hash()) }()
	select {
	case tx := <-done:
		if tx != old {
			pool.mu.Unlock()
			t.Fatal("lost target")
		}
	case <-time.After(time.Second):
		pool.mu.Unlock()
		t.Fatal("hash lookup waited on reset lock")
	}
	// Match the production replacement order under the reset lock.
	pool.all.Remove(old.Hash())
	pool.all.Add(next)
	if pool.Get(old.Hash()) != nil || pool.Get(next.Hash()) != next {
		pool.mu.Unlock()
		t.Fatal("stale replacement index")
	}
	pool.mu.Unlock()
}
