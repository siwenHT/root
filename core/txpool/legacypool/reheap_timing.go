package legacypool

import (
	"container/heap"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/types"
)

const reheapSampleEvery = 32

type reheapTiming struct {
	stats      pendingtrace.ReheapStats
	id         uint64
	head       common.Hash
	begin, end time.Time
}

// Called under pool.mu, once for each actual head-reset price rebuild.
func (pool *LegacyPool) sampleReheap(head *types.Header) *reheapTiming {
	pool.reheapSequence++
	if pool.reheapSequence%reheapSampleEvery != 0 {
		return nil
	}
	return &reheapTiming{
		id: pendingtrace.NextID(), head: head.Hash(),
		stats: pendingtrace.ReheapStats{Version: 1, Rebuild: pool.reheapSequence, SampleEvery: reheapSampleEvery,
			Head: head.Number.Uint64(), Queued: pool.queue.stats(), PendingAccounts: len(pool.pending), QueuedAccounts: len(pool.queue.queued)},
	}
}

// Caller releases both pool.mu and reheapMu before publishing to the ring.
func (d *reheapTiming) publish(lock poolLockTiming) {
	if d == nil {
		return
	}
	pendingtrace.Span(pendingtrace.Event{Scope: "reheap", Stage: "priced_reheap", Source: "head_reset",
		Pool: lock.pool.traceID.Load(), Operation: d.id, ParentOp: lock.id, Parent: d.head, Reheap: &d.stats}, d.begin, d.end)
}

func reheapTypeBucket(kind byte) int {
	if kind <= types.SetCodeTxType {
		return int(kind)
	}
	return 5
}

// Only sampled rounds use this wrapper. priceHeap.Less/cmp and the normal
// transaction comparator remain untouched, including their inlining behavior.
type diagnosticPriceHeap struct {
	*priceHeap
	stats *pendingtrace.ReheapStage
}

func (h *diagnosticPriceHeap) Less(i, j int) bool {
	a, b := h.list[i], h.list[j]
	s := h.stats
	s.Comparisons++
	s.TypePairs[reheapTypeBucket(a.Type())][reheapTypeBucket(b.Type())]++
	if h.baseFee != nil {
		cmp, path := a.EffectiveGasTipCmpWithPath(b, h.baseFee)
		switch path {
		case types.GasTipCmpZeroFast:
			s.ZeroFast++
		case types.GasTipCmpZeroFallback:
			s.ZeroFallback++
		case types.GasTipCmpNonzero:
			s.Nonzero++
		case types.GasTipCmpNil:
			s.NilBaseFee++
		}
		if cmp != 0 {
			return cmp < 0
		}
		s.EffectiveTipEqual++
	} else {
		s.NilBaseFee++
	}
	if cmp := a.GasFeeCapCmp(b); cmp != 0 {
		return cmp < 0
	}
	s.FeeCapEqual++
	if cmp := a.GasTipCapCmp(b); cmp != 0 {
		return cmp < 0
	}
	s.TipCapEqual++
	s.NonceCompared++
	nonceA, nonceB := a.Nonce(), b.Nonce()
	if nonceA == nonceB {
		s.NonceEqual++
	}
	return nonceA > nonceB
}

func (l *pricedList) rebuildDiagnosticHeaps(d *reheapTiming, lookupEnd time.Time) {
	urgent := &diagnosticPriceHeap{&l.urgent, &d.stats.Stages[0]}
	heap.Init(urgent)
	initEnd := time.Now()
	d.stats.Stages[0].DurationUS = initEnd.Sub(lookupEnd).Microseconds()
	urgent.stats = &d.stats.Stages[1]
	floatingCount := len(l.urgent.list) * floatingRatio / (urgentRatio + floatingRatio)
	l.floating.list = make([]*types.Transaction, floatingCount)
	for i := range l.floating.list {
		l.floating.list[i] = heap.Pop(urgent).(*types.Transaction)
	}
	popEnd := time.Now()
	d.stats.Stages[1].DurationUS = popEnd.Sub(initEnd).Microseconds()
	floating := &diagnosticPriceHeap{&l.floating, &d.stats.Stages[2]}
	heap.Init(floating)
	d.stats.Stages[2].DurationUS = time.Since(popEnd).Microseconds()
}
