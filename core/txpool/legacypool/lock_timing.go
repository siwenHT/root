package legacypool

import (
	"time"

	"github.com/ethereum/go-ethereum/common/pendingtrace"
)

type poolLockTiming struct {
	pool           *LegacyPool
	id             uint64
	source         string
	read           bool
	packet         *pendingtrace.Packet
	parent         uint64
	wait, acquired time.Time
}

func (pool *LegacyPool) lockTiming(source string, read bool, id uint64) poolLockTiming {
	if id == 0 {
		id = pendingtrace.NextID()
	}
	if pool.traceID.Load() == 0 {
		pool.traceID.CompareAndSwap(0, pendingtrace.NextID())
	}
	t := poolLockTiming{pool: pool, id: id, source: source, read: read, wait: time.Now()}
	if read {
		pool.mu.RLock()
	} else {
		pool.mu.Lock()
	}
	t.acquired = time.Now()
	return t
}

func (t poolLockTiming) event(stage string) pendingtrace.Event {
	kind := "writer"
	if t.read {
		kind = "reader"
	}
	return t.packet.Event(pendingtrace.Event{Scope: "lock", Pool: t.pool.traceID.Load(), Operation: t.id, ParentOp: t.parent, Source: t.source, Outcome: kind, Stage: stage})
}

// The hold endpoint is captured before Unlock: it is a confirmed held interval,
// not a claim about which nanosecond another waiter was granted the mutex.
// Publication is strictly after unlock, including for unsampled Add calls.
func (t poolLockTiming) unlock() time.Time {
	holdEnd := time.Now()
	if t.read {
		t.pool.mu.RUnlock()
	} else {
		t.pool.mu.Unlock()
	}
	released := time.Now()
	pendingtrace.Span(t.event("pool_lock_wait"), t.wait, t.acquired)
	pendingtrace.Span(t.event("pool_lock_hold"), t.acquired, holdEnd)
	return released
}

type poolPhaseTiming struct {
	name       string
	begin, end time.Time
	outcome    string
}

func (t poolLockTiming) phase(p poolPhaseTiming) {
	e := t.event("pool_phase")
	e.Source, e.Outcome = p.name, p.outcome
	pendingtrace.Span(e, p.begin, p.end)
}
