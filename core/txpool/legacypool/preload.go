package legacypool

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
)

// poolStateProvider must transfer a fresh StateDB exclusively to the caller.
// StateAt alone does not promise this (some callers return shared mutable state).
// Wrappers must opt in explicitly, rather than inherit a capability from the
// BlockChain interface. Only the underlying thread-safe Reader may be shared.
type poolStateProvider interface {
	StateAtForPool(common.Hash) (*state.StateDB, error)
}

type resetPreload struct {
	oldHead, target common.Hash
	root            common.Hash
	previous        *state.StateDB
	previousNonces  *noncer
	state           *state.StateDB
	nonces          *noncer
	begin           time.Time
	stats           *pendingtrace.PreloadStats
	outcome         string
}

// The existing runReorg goroutine is the sole owner throughout preparation and
// adoption. No mutable pool state or lists leave the snapshot's read lock.
func (pool *LegacyPool) preloadReset(reset *txpoolResetRequest, events map[common.Address]*SortedMap) *resetPreload {
	provider, ok := pool.chain.(poolStateProvider)
	if !ok || reset == nil || reset.oldHead == nil || reset.newHead == nil ||
		reset.newHead.ParentHash != reset.oldHead.Hash() ||
		reset.newHead.Number.Cmp(new(big.Int).Add(reset.oldHead.Number, common.Big1)) != 0 {
		return nil
	}
	p := &resetPreload{oldHead: reset.oldHead.Hash(), target: reset.newHead.Hash(), root: reset.newHead.Root, begin: time.Now(), outcome: "prepared"}
	pool.mu.RLock()
	acquired := time.Now()
	if (pool.reheapSequence+1)%reheapSampleEvery == 0 {
		p.stats = &pendingtrace.PreloadStats{Version: 1, Rebuild: pool.reheapSequence + 1, SampleEvery: reheapSampleEvery,
			Root: p.root, SnapshotWaitUS: acquired.Sub(p.begin).Microseconds()}
	}
	if head := pool.currentHead.Load(); head == nil || head.Hash() != p.oldHead {
		if p.stats != nil {
			p.stats.SnapshotHoldUS = time.Since(acquired).Microseconds()
		}
		pool.mu.RUnlock()
		p.outcome = "old_head_changed"
		return p
	}
	p.previous, p.previousNonces = pool.currentState, pool.pendingNonces
	// Pending and queued maps can overlap. Value-only deduplication also covers
	// this run's notifications, without retaining mutable lists off lock.
	addresses := make(map[common.Address]struct{}, len(pool.pending)+len(pool.queue.queued)+len(events))
	for addr := range pool.pending {
		addresses[addr] = struct{}{}
	}
	for addr := range pool.queue.queued {
		addresses[addr] = struct{}{}
	}
	for addr := range events {
		addresses[addr] = struct{}{}
	}
	snapshotEnd := p.now()
	pool.mu.RUnlock()
	opened := p.now()
	if p.stats != nil {
		p.stats.SnapshotHoldUS = snapshotEnd.Sub(acquired).Microseconds()
		p.stats.Accounts = len(addresses)
	}

	statedb, err := provider.StateAtForPool(p.root)
	stateEnd := p.now()
	if p.stats != nil {
		p.stats.StateOpenUS = stateEnd.Sub(opened).Microseconds()
	}
	if err != nil || statedb == nil {
		p.outcome = "state_open_error"
		return p
	}
	if statedb == p.previous || statedb == p.previousNonces.fallback {
		p.outcome = "shared_state"
		return p
	}
	if statedb.Error() != nil {
		p.outcome = "state_read_error"
		if p.stats != nil {
			p.stats.ReadErrors = 1
		}
		return p
	}
	p.state = statedb
	// Copy BEFORE any warmup: copying a warm StateDB deep-copies every object.
	p.nonces = newNoncer(statedb)
	noncerEnd := p.now()
	if p.stats != nil {
		p.stats.NoncerUS = noncerEnd.Sub(stateEnd).Microseconds()
		// Keep partial attempts and time even when warming fails.
		defer func() {
			p.stats.ReadUS = time.Since(noncerEnd).Microseconds()
			p.stats.Loaded = statedb.AccountLoaded
			p.stats.AccountReadsUS = statedb.AccountReads.Microseconds()
			if statedb.Error() != nil {
				p.stats.ReadErrors = 1
			}
		}()
	}
	for addr := range addresses {
		statedb.GetNonce(addr) // Also loads the account's balance.
		if statedb.Error() != nil {
			p.state, p.nonces = nil, nil
			p.outcome = "state_read_error"
			return p
		}
	}
	return p
}

// Called only with the pool write lock held. Retaining both old pointers also
// prevents ABA; Clear replaces pendingNonces even when head/state are unchanged.
func (p *resetPreload) valid(pool *LegacyPool, oldHead, newHead *types.Header) bool {
	return p != nil && p.state != nil && oldHead != nil && newHead != nil &&
		p.oldHead == oldHead.Hash() && p.target == newHead.Hash() && p.root == newHead.Root &&
		pool.currentHead.Load().Hash() == p.oldHead &&
		pool.currentState == p.previous && pool.pendingNonces == p.previousNonces
}

func (p *resetPreload) now() time.Time {
	if p.stats == nil {
		return time.Time{}
	}
	return time.Now()
}

// Called under the write lock; no StateDB is touched by the later publication.
func (p *resetPreload) finishLocked(lock poolLockTiming) {
	if p == nil || p.stats == nil {
		return
	}
	p.stats.WriteWaitUS = lock.acquired.Sub(lock.wait).Microseconds()
	p.stats.WriteHoldUS = time.Since(lock.acquired).Microseconds()
	if p.outcome == "adopted" {
		p.stats.TailLoads = p.state.AccountLoaded - p.stats.Loaded
	}
}

func (p *resetPreload) publish(lock poolLockTiming) {
	if p == nil || p.stats == nil {
		return
	}
	end := time.Now()
	p.stats.RunToCompleteUS = end.Sub(p.begin).Microseconds()
	pendingtrace.Span(pendingtrace.Event{Scope: "preload", Stage: "state_preload", Source: "head_reset", Outcome: p.outcome,
		Pool: lock.pool.traceID.Load(), Operation: lock.id, Parent: p.target, Preload: p.stats}, p.begin, end)
}
