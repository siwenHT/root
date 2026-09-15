package eth

import (
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/eth/arb"
	"github.com/ethereum/go-ethereum/params"
	"sync"
	"testing"
	"time"
)

func TestExecutorV3HandleConcurrencyAndParentChildCleanup(t *testing.T) {
	bc, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), &core.Genesis{Config: params.TestChainConfig}, ethash.NewFaker(), core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Stop()
	eth := &Ethereum{blockchain: bc}
	svc, err := eth.newArbService(arbServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	parent := bc.GetHeaderByNumber(0).Hash()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				ident := arb.HandleIdentity{OwnerSession: arb.RandomID32(), NodeBootID: svc.boot, Identity: arb.RandomID32()}
				id, _, err := svc.pinParent(ident, parent, time.Second)
				if err != nil {
					t.Error(err)
					return
				}
				// Different job ID must be usable with the same bearer handle.
				ident.Identity = arb.RandomID32()
				base, header, release, err := svc.borrowBase(id, ident)
				if err != nil {
					t.Error(err)
					return
				}
				child := svc.registerPostChild(id, ident, base, header)
				release()
				if child == "" {
					t.Error("child registration failed")
					return
				}
				if _, err := svc.releaseParent(id); err != nil {
					t.Error(err)
					return
				}
				_, _, release, err = svc.borrowBase(child, ident)
				if err != nil {
					t.Error(err)
					return
				}
				release()
				if _, err := svc.releaseParent(child); err != nil {
					t.Error(err)
					return
				}
				if _, err := svc.releaseParent(child); err != nil {
					t.Error("idempotent release", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if svc.handles.Size() != 0 || len(svc.handleStates.states) != 0 {
		t.Fatalf("leaked handles: %d states: %d", svc.handles.Size(), len(svc.handleStates.states))
	}
}
