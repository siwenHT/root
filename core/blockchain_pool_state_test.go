package core

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestStateAtForPoolExclusive(t *testing.T) {
	bc := &BlockChain{statedb: state.NewDatabaseForTesting()}
	first, err := bc.StateAtForPool(types.EmptyRootHash)
	if err != nil {
		t.Fatal(err)
	}
	second, err := bc.StateAtForPool(types.EmptyRootHash)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("mutable StateDB shared")
	}
	addr := common.Address{1}
	var wg sync.WaitGroup
	for i, s := range []*state.StateDB{first, second} {
		wg.Add(1)
		go func(i int, s *state.StateDB) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				s.SetNonce(addr, uint64(i+1), tracing.NonceChangeUnspecified)
				s.GetBalance(addr)
			}
		}(i, s)
	}
	wg.Wait()
	if first.GetNonce(addr) != 1 || second.GetNonce(addr) != 2 {
		t.Fatal("mutable objects leaked across exclusive views")
	}
	if _, err := bc.StateAtForPool(common.HexToHash("dead")); err == nil {
		t.Fatal("missing root accepted")
	}
}
