// Package pendingtrace keeps sampled pending-transaction timing evidence in memory.
// It does not log payloads or change transaction processing or RPC notifications.
package pendingtrace

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

const (
	SampleEvery = 32
	Capacity    = 65536
	MaxPage     = 1024
)

// Event is one boundary, not a claim that all boundaries form a linear path.
// Add return, asynchronous promotion, simulations and firstSeen may overlap.
type Event struct {
	Sequence     uint64      `json:"sequence"`
	Hash         common.Hash `json:"hash"`
	Stage        string      `json:"stage"`
	UnixUS       int64       `json:"unix_us"`
	MonotonicUS  int64       `json:"monotonic_us"`
	Subscription string      `json:"subscription,omitempty"`
	RPCSub       string      `json:"rpc_subscription,omitempty"`
	Attempt      uint64      `json:"attempt,omitempty"`
	Operation    uint64      `json:"operation,omitempty"`
	Source       string      `json:"source,omitempty"`
	Outcome      string      `json:"outcome,omitempty"`
	Parent       common.Hash `json:"parent"`
	WaitUS       int64       `json:"wait_us,omitempty"`
	SlackUS      int64       `json:"slack_us,omitempty"`
	Scope        string      `json:"scope,omitempty"`
	DurationUS   int64       `json:"duration_us,omitempty"`
	Pool         uint64      `json:"pool,omitempty"`
	ParentOp     uint64      `json:"parent_operation,omitempty"`
	Connection   uint64      `json:"connection,omitempty"`
	Packet       uint64      `json:"packet,omitempty"`
	PacketNumber uint64      `json:"packet_number,omitempty"`
	Previous     uint64      `json:"previous_packet,omitempty"`
	Protocol     string      `json:"protocol,omitempty"`
	Version      uint        `json:"protocol_version,omitempty"`
	Code         uint64      `json:"message_code,omitempty"`
	Buffered     int         `json:"buffered_bytes,omitempty"`
	FrameBytes   int         `json:"frame_bytes,omitempty"`
	BatchSize    int         `json:"batch_size,omitempty"`
	Rejected     int         `json:"rejected,omitempty"`

	Reheap *ReheapStats `json:"reheap,omitempty"`
}

type Page struct {
	Schema      int     `json:"schema_version"`
	BootID      string  `json:"boot_id"`
	SampleEvery int     `json:"sample_every"`
	Capacity    int     `json:"capacity"`
	Oldest      uint64  `json:"oldest_sequence"`
	Latest      uint64  `json:"latest_sequence"`
	Next        uint64  `json:"next_sequence"`
	Overwritten uint64  `json:"overwritten"`
	Lost        uint64  `json:"lost_since_cursor"`
	Events      []Event `json:"events"`

	ReheapRecorded    uint64 `json:"reheap_recorded"`
	ReheapOverwritten uint64 `json:"reheap_overwritten"`
}

type recorder struct {
	mu     sync.Mutex
	start  time.Time
	bootID string
	ring   []Event
	latest uint64

	reheapRecorded, reheapOverwritten uint64
}

var (
	defaultRecorder = newRecorder(Capacity, time.Now())
	identifiers     atomic.Uint64
)

func newRecorder(capacity int, start time.Time) *recorder {
	return &recorder{start: start, bootID: fmt.Sprintf("%d-%x", os.Getpid(), start.UnixNano()), ring: make([]Event, capacity)}
}

// Sampled chooses the same hashes at ingress, admission and every subscription.
func Sampled(hash common.Hash) bool { return hash[0]&(SampleEvery-1) == 0 }

// NextID identifies an admission operation, delivery batch or simulation attempt.
func NextID() uint64 { return identifiers.Add(1) }

func Record(event Event) {
	if Sampled(event.Hash) {
		defaultRecorder.recordAt(event, time.Now())
	}
}

// RecordAt reuses an existing timestamp, including the P2P message receive time.
// A missing timestamp stays missing; it must not become a zero-duration span.
func RecordAt(event Event, at time.Time) {
	if Sampled(event.Hash) && !at.IsZero() {
		defaultRecorder.recordAt(event, at)
	}
}

// Span uses its end as the event time. Scoped records use a zero hash and are
// not transactions. Callers publish lock spans only after releasing the lock.
func Span(event Event, begin, end time.Time) {
	if !begin.IsZero() && !end.IsZero() && !end.Before(begin) {
		event.DurationUS = end.Sub(begin).Microseconds()
		RecordAt(event, end)
	}
}

func (r *recorder) recordAt(event Event, at time.Time) {
	if event.Reheap != nil {
		payload := *event.Reheap
		event.Reheap = &payload
	}
	event.UnixUS = at.UnixMicro()
	event.MonotonicUS = at.Sub(r.start).Microseconds()
	r.mu.Lock()
	r.latest++
	event.Sequence = r.latest
	index := (r.latest - 1) % uint64(len(r.ring))
	if r.ring[index].Reheap != nil {
		r.reheapOverwritten++
	}
	if event.Reheap != nil {
		r.reheapRecorded++
	}
	r.ring[index] = event
	r.mu.Unlock()
}

// Snapshot returns at most MaxPage events without clearing or advancing state.
// Consumers paginate with Next, detect cursor gaps with Lost, and reset on BootID.
func Snapshot(after uint64, limit int) Page { return defaultRecorder.snapshot(after, limit) }

func (r *recorder) snapshot(after uint64, limit int) Page {
	if limit <= 0 || limit > MaxPage {
		limit = MaxPage
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := Page{Schema: 2, BootID: r.bootID, SampleEvery: SampleEvery, Capacity: len(r.ring), Latest: r.latest, Next: after, Events: make([]Event, 0, limit)}
	p.ReheapRecorded, p.ReheapOverwritten = r.reheapRecorded, r.reheapOverwritten
	if r.latest == 0 {
		return p
	}
	p.Oldest = 1
	if r.latest > uint64(len(r.ring)) {
		p.Overwritten = r.latest - uint64(len(r.ring))
		p.Oldest = p.Overwritten + 1
	}
	if after >= r.latest {
		return p
	}
	first := after + 1
	if first < p.Oldest {
		p.Lost = p.Oldest - first
		first = p.Oldest
	}
	last := min(r.latest, first+uint64(limit)-1)
	for seq := first; seq <= last; seq++ {
		event := r.ring[(seq-1)%uint64(len(r.ring))]
		if event.Reheap != nil {
			payload := *event.Reheap
			event.Reheap = &payload
		}
		p.Events = append(p.Events, event)
	}
	p.Next = last
	return p
}

// Attempt is immutable and can follow a result through workers and RPC buffering.
// Subscription is the EventSystem ID; RPCSub links it to the client-visible ID.
type Attempt struct {
	Hash         common.Hash
	Subscription string
	RPCSub       string
	ID           uint64
	Batch        uint64
}

func NewAttempt(hash common.Hash, subscription, rpcSub string, batch uint64) *Attempt {
	if !Sampled(hash) {
		return nil
	}
	return &Attempt{Hash: hash, Subscription: subscription, RPCSub: rpcSub, ID: NextID(), Batch: batch}
}

func (a *Attempt) Record(stage, outcome string, at time.Time) {
	if a != nil {
		RecordAt(Event{Hash: a.Hash, Subscription: a.Subscription, RPCSub: a.RPCSub, Attempt: a.ID, Operation: a.Batch, Stage: stage, Outcome: outcome}, at)
	}
}

// Mark avoids reading the clock for an unsampled attempt.
func (a *Attempt) Mark(stage, outcome string) {
	if a != nil {
		a.Record(stage, outcome, time.Now())
	}
}

type attemptKey struct{}

func WithAttempt(ctx context.Context, a *Attempt) context.Context {
	if a == nil {
		return ctx
	}
	return context.WithValue(ctx, attemptKey{}, a)
}

func FromContext(ctx context.Context) *Attempt {
	a, _ := ctx.Value(attemptKey{}).(*Attempt)
	return a
}

func State(ctx context.Context, parent common.Hash, source string) {
	if a := FromContext(ctx); a != nil {
		Record(Event{Hash: a.Hash, Subscription: a.Subscription, RPCSub: a.RPCSub, Attempt: a.ID, Stage: "simulation_state_ready", Parent: parent, Source: source})
	}
}
