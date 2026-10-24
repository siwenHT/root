package legacypool

import (
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

type timingStateChain struct {
	BlockChain
	entered, release chan struct{}
	fail             bool
}

func (c *timingStateChain) StateAt(root common.Hash) (*state.StateDB, error) {
	close(c.entered)
	<-c.release
	if c.fail {
		return nil, errors.New("state unavailable")
	}
	return c.BlockChain.StateAt(root)
}

func TestPendingLockReorgPhasesPublishAfterUnlock(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "state_error"}[fail], func(t *testing.T) {
			pool, _ := setupPoolWithConfig(params.TestChainConfig)
			defer pool.Close()
			chain := &timingStateChain{BlockChain: pool.chain, entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
			pool.chain = chain
			before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
			done := make(chan struct{})
			head := types.CopyHeader(pool.currentHead.Load())
			head.BaseFee = big.NewInt(1)
			go pool.runReorg(done, &txpoolResetRequest{newHead: head}, nil, nil)
			<-chain.entered
			for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
				if e.Stage == "pool_phase" || (e.Scope == "lock" && e.Source == "runReorg") {
					t.Fatal("diagnostic publication occurred inside the pool lock")
				}
			}
			time.Sleep(15 * time.Millisecond)
			close(chain.release)
			<-done
			phases := make(map[string]pendingtrace.Event)
			var hold pendingtrace.Event
			for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
				if e.Stage == "pool_phase" {
					phases[e.Source] = e
				}
				if e.Stage == "pool_lock_hold" && e.Source == "runReorg" {
					hold = e
				}
			}
			for _, name := range []string{"reset", "state_open", "promote", "demote", "priced_reheap", "pending_nonce_rebuild", "truncate_pending", "truncate_queue"} {
				e := phases[name]
				if e.Sequence == 0 || e.Pool == 0 || e.Pool != hold.Pool || e.Operation != hold.Operation || e.MonotonicUS > hold.MonotonicUS || e.MonotonicUS-e.DurationUS < hold.MonotonicUS-hold.DurationUS {
					t.Fatalf("phase escaped its actual lock interval: %s %+v hold=%+v", name, e, hold)
				}
			}
			want := "ok"
			if fail {
				want = "error"
			}
			if phases["state_open"].Outcome != want || phases["state_open"].DurationUS < 10000 {
				t.Fatalf("state opening not isolated: %+v", phases["state_open"])
			}
		})
	}
}

func TestPendingLockRecordsUnsampledAdmissionAndReaders(t *testing.T) {
	pool, key := setupPoolWithConfig(params.TestChainConfig)
	defer pool.Close()
	pool.currentState.AddBalance(crypto.PubkeyToAddress(key.PublicKey), uint256.NewInt(1000000000000000000), tracing.BalanceChangeUnspecified)
	var tx *types.Transaction
	for price := int64(1); ; price++ {
		tx = pricedTransaction(0, 21000, big.NewInt(price), key)
		if !pendingtrace.Sampled(tx.Hash()) {
			break
		}
	}
	packet := pendingtrace.NewPacket(pendingtrace.PacketMeta{Connection: 31, Number: 1})
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	reader := pool.lockTiming("test_reader", true, 0)
	done := make(chan []error, 1)
	started := make(chan struct{})
	go func() { close(started); done <- pool.AddWithTrace([]*types.Transaction{tx}, false, packet, 99) }()
	<-started
	time.Sleep(20 * time.Millisecond)
	reader.unlock()
	if errs := <-done; len(errs) != 1 || errs[0] != nil {
		t.Fatalf("admission changed: %v", errs)
	}
	var wait, read pendingtrace.Event
	for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
		if e.Stage == "pool_lock_wait" && e.Source == "Add" {
			wait = e
		}
		if e.Stage == "pool_lock_hold" && e.Source == "test_reader" {
			read = e
		}
	}
	if wait.DurationUS < 15000 || wait.Packet != packet.ID || wait.ParentOp != 99 || read.Outcome != "reader" || wait.Pool != read.Pool || wait.Pool == 0 {
		t.Fatalf("unsampled owner/context missing: %+v %+v", wait, read)
	}
}

func BenchmarkPendingLockTiming(b *testing.B) {
	pool := new(LegacyPool)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		lock := pool.lockTiming("benchmark", false, 0)
		lock.unlock()
	}
}

func TestPendingLockDoesNotInventSkippedReorgPhases(t *testing.T) {
	for _, reset := range []*txpoolResetRequest{nil, {}} {
		pool, _ := setupPoolWithConfig(params.TestChainConfig)
		before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
		done := make(chan struct{})
		pool.runReorg(done, reset, nil, nil)
		for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
			if e.Stage == "pool_phase" && (e.Source == "priced_reheap" || (reset == nil && e.Source == "state_open")) {
				t.Fatalf("skipped work was recorded as a zero duration phase: %+v", e)
			}
		}
		pool.Close()
	}
}
