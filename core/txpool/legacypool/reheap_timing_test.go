package legacypool

import (
	"container/heap"
	"math"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

type countedPriceHeap struct {
	*priceHeap
	comparisons *uint64
}

func (h *countedPriceHeap) Less(i, j int) bool { *h.comparisons++; return h.priceHeap.Less(i, j) }

func referenceRebuild(p *pricedList) [3]uint64 {
	var counts [3]uint64
	urgent := &countedPriceHeap{&p.urgent, &counts[0]}
	heap.Init(urgent)
	urgent.comparisons = &counts[1]
	p.floating.list = make(types.Transactions, len(p.urgent.list)/5)
	for i := range p.floating.list {
		p.floating.list[i] = heap.Pop(urgent).(*types.Transaction)
	}
	heap.Init(&countedPriceHeap{&p.floating, &counts[2]})
	return counts
}

func checkReheapCounters(t *testing.T, d *reheapTiming) {
	t.Helper()
	for i, s := range d.stats.Stages {
		var pairs uint64
		for _, row := range s.TypePairs {
			for _, n := range row {
				pairs += n
			}
		}
		if pairs != s.Comparisons || s.Comparisons != s.NilBaseFee+s.ZeroFast+s.ZeroFallback+s.Nonzero || s.TipCapEqual != s.NonceCompared || s.NonceEqual > s.NonceCompared || s.TipCapEqual > s.FeeCapEqual || s.FeeCapEqual > s.NilBaseFee+s.EffectiveTipEqual || s.DurationUS < 0 {
			t.Fatalf("stage %d counters do not close: %+v", i, s)
		}
	}
}

func checkFrozenReheap(t *testing.T, txs types.Transactions, base *uint256.Int) {
	t.Helper()
	old, current := newPricedList(newLookup()), newPricedList(newLookup())
	for _, tx := range txs {
		old.all.Add(tx)
		current.all.Add(tx)
	}
	old.urgent.baseFee, current.urgent.baseFee = base, base
	old.urgent.list, current.urgent.list = slices.Clone(txs), slices.Clone(txs)
	want := referenceRebuild(old)
	d := new(reheapTiming)
	current.rebuildDiagnosticHeaps(d, time.Now())
	checkReheapCounters(t, d)
	for i, stage := range d.stats.Stages {
		if stage.Comparisons != want[i] {
			t.Fatalf("stage %d comparisons %d want %d", i, stage.Comparisons, want[i])
		}
	}
	for i, pair := range [][2]types.Transactions{{old.urgent.list, current.urgent.list}, {old.floating.list, current.floating.list}} {
		if !slices.Equal(pair[0], pair[1]) {
			t.Fatalf("heap %d membership/order differs", i)
		}
	}
	// Remove a stale minimum before eviction. Keep below Removed's reheap
	// threshold: this test deliberately freezes the original map iteration order.
	if len(txs) > 20 {
		stale := old.floating.list[0]
		for _, p := range []*pricedList{old, current} {
			p.all.Remove(stale.Hash())
			p.Removed(1)
		}
	}
	for _, slots := range []int{1, 3, 7, len(txs) + 10, 5} {
		for _, probe := range txs[:min(5, len(txs))] {
			if old.Underpriced(probe) != current.Underpriced(probe) {
				t.Fatal("underpriced differs")
			}
		}
		a, okA := old.Discard(slots)
		b, okB := current.Discard(slots)
		if okA != okB || !slices.Equal(a, b) {
			t.Fatalf("eviction/rollback differs for %d slots", slots)
		}
		for _, tx := range a {
			old.all.Remove(tx.Hash())
			current.all.Remove(tx.Hash())
		}
	}
}

func TestReheapDiagnosticSortingEvictionAndCounts(t *testing.T) {
	var txs types.Transactions
	for i := 0; i < 192; i++ {
		var data []byte
		if i%17 == 0 {
			data = make([]byte, txSlotSize+1)
		}
		txs = append(txs, comparisonTransaction(i, uint64(i%5), uint64(i%7), data))
	}
	// Realistic full-priority ties: distinct txs with the same nonce and price.
	for i := 0; i < 12; i++ {
		txs = append(txs, types.NewTx(&types.LegacyTx{Nonce: 0, GasPrice: big.NewInt(1), Data: []byte{byte(i)}}))
	}
	for _, base := range []*uint256.Int{nil, new(uint256.Int), uint256.NewInt(3)} {
		checkFrozenReheap(t, txs, base)
	}
	checkFrozenReheap(t, nil, new(uint256.Int))
	checkFrozenReheap(t, txs[:1], new(uint256.Int))
}

func TestReheapRealSnapshotDifferential(t *testing.T) {
	for _, seed := range []int64{1, 25, 9025} {
		txs := reheapRealTransactions(t, 39168, seed)
		for _, base := range []*uint256.Int{nil, new(uint256.Int), uint256.NewInt(7)} {
			checkFrozenReheap(t, txs, base)
		}
	}
}

func TestDiagnosticHeapMalformedCapBranches(t *testing.T) {
	txs := types.Transactions{
		types.NewTx(&types.LegacyTx{GasPrice: big.NewInt(-1)}),
		types.NewTx(&types.LegacyTx{GasPrice: new(big.Int).Lsh(big.NewInt(1), 256)}),
		types.NewTx(&types.DynamicFeeTx{GasFeeCap: big.NewInt(2), GasTipCap: big.NewInt(7)}),
		types.NewTx(&types.DynamicFeeTx{GasFeeCap: big.NewInt(-1), GasTipCap: big.NewInt(1)}),
		comparisonTransaction(0, 3, 3, nil),
	}
	for _, base := range []*uint256.Int{nil, new(uint256.Int), uint256.NewInt(3)} {
		plain := &priceHeap{baseFee: base, list: slices.Clone(txs)}
		d := new(reheapTiming)
		instrumented := &diagnosticPriceHeap{&priceHeap{baseFee: base, list: slices.Clone(txs)}, &d.stats.Stages[0]}
		heap.Init(plain)
		heap.Init(instrumented)
		for plain.Len() > 0 {
			if heap.Pop(plain) != heap.Pop(instrumented) {
				t.Fatal("malformed-cap sorting changed")
			}
		}
		checkReheapCounters(t, d)
		if base != nil && base.IsZero() && d.stats.Stages[0].ZeroFallback == 0 {
			t.Fatal("guard fallback was not counted")
		}
	}
	if reheapTypeBucket(255) != 5 {
		t.Fatal("unknown transaction type escaped bounded bucket")
	}
}

func TestReheapSamplingAndEmptyInput(t *testing.T) {
	pool := &LegacyPool{queue: newQueue(DefaultConfig, nil)}
	head := &types.Header{Number: big.NewInt(12)}
	sampled := 0
	for i := 1; i <= 64; i++ {
		d := pool.sampleReheap(head)
		if (d != nil) != (i%32 == 0) {
			t.Fatalf("round %d sampled incorrectly", i)
		}
		if d == nil {
			continue
		}
		sampled++
		p := newPricedList(newLookup())
		p.stales.Store(9)
		p.reheap(d)
		if d.stats.Rebuild != uint64(i) || d.stats.SampleEvery != 32 || d.stats.Total != 0 || d.stats.StalesBefore != 9 || p.stales.Load() != 0 || d.begin.IsZero() || d.end.Before(d.begin) {
			t.Fatalf("bad empty sample %+v", d)
		}
		checkReheapCounters(t, d)
	}
	if sampled != 2 {
		t.Fatal("wrong sample denominator")
	}
}

func TestReheapFullInputAndMutexWait(t *testing.T) {
	p := reheapBenchmarkList(reheapRealTransactions(t, 39168, 25))
	d := &reheapTiming{stats: pendingtrace.ReheapStats{Queued: 738}}
	p.reheapMu.Lock()
	done := make(chan struct{})
	started := make(chan struct{})
	go func() { close(started); p.reheap(d); close(done) }()
	<-started
	time.Sleep(20 * time.Millisecond)
	p.reheapMu.Unlock()
	<-done
	if d.stats.MutexWaitUS < 1000 || d.stats.Total != 39168 || d.stats.Pending != 38430 || d.stats.UrgentAfter != 31335 || d.stats.FloatingAfter != 7833 {
		t.Fatalf("wrong size/wait %+v", d.stats)
	}
	if d.stats.TypeCounts != [6]uint64{34241, 340, 4575, 0, 12, 0} {
		t.Fatalf("wrong real types %v", d.stats.TypeCounts)
	}
	var stageUS int64
	for _, stage := range d.stats.Stages {
		stageUS += stage.DurationUS
	}
	if d.stats.TotalUS < d.stats.LookupUS+stageUS {
		t.Fatal("stage times exceed total")
	}
	for _, stage := range d.stats.Stages[:2] {
		if stage.Comparisons != stage.ZeroFast {
			t.Fatal("eligible comparisons missed the actual fast branch")
		}
	}
	if d.stats.Stages[2].Comparisons != d.stats.Stages[2].NilBaseFee {
		t.Fatal("floating heap inherited urgent base fee")
	}
	checkReheapCounters(t, d)
}

func TestReheapPublicationAfterLocks(t *testing.T) {
	for _, fail := range []bool{false, true} {
		pool, _ := setupPoolWithConfig(params.TestChainConfig)
		chain := &timingStateChain{BlockChain: pool.chain, entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
		pool.chain = chain
		pool.mu.Lock()
		pool.reheapSequence = 31
		pool.mu.Unlock()
		before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
		head := types.CopyHeader(pool.currentHead.Load())
		head.BaseFee = new(big.Int)
		done := make(chan struct{})
		go pool.runReorg(done, &txpoolResetRequest{newHead: head}, nil, nil)
		<-chain.entered
		for _, event := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
			if event.Reheap != nil {
				t.Fatal("reheap record published while pool lock held")
			}
		}
		close(chain.release)
		<-done
		var hold, event pendingtrace.Event
		for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
			if e.Pool != pool.traceID.Load() {
				continue
			}
			if e.Stage == "pool_lock_hold" && e.Source == "runReorg" {
				hold = e
			}
			if e.Reheap != nil {
				event = e
			}
		}
		if event.Sequence <= hold.Sequence || event.ParentOp != hold.Operation || event.Operation == hold.Operation || event.Pool == 0 || event.Parent != head.Hash() || event.Reheap.Rebuild != 32 || event.Reheap.Version != 1 {
			t.Fatalf("correlation/publication missing: %+v hold=%+v", event, hold)
		}
		if !pool.mu.TryLock() {
			t.Fatal("pool mutex remains locked")
		}
		pool.mu.Unlock()
		if !pool.priced.reheapMu.TryLock() {
			t.Fatal("reheap mutex remains locked")
		}
		pool.priced.reheapMu.Unlock()
		pool.Close()
	}
}

func TestReheapSkippedHeadDoesNotCount(t *testing.T) {
	pool, _ := setupPoolWithConfig(params.TestChainConfig)
	defer pool.Close()
	for _, reset := range []*txpoolResetRequest{nil, {}} {
		before := pool.reheapSequence
		done := make(chan struct{})
		pool.runReorg(done, reset, nil, nil)
		if pool.reheapSequence != before {
			t.Fatal("skipped rebuild advanced the denominator")
		}
	}
}
