package pendingtrace

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func TestSamplingAndMissingTimestamp(t *testing.T) {
	count := 0
	for n := 0; n < 256; n++ {
		if Sampled(common.Hash{byte(n)}) {
			count++
		}
	}
	if count != 256/SampleEvery {
		t.Fatalf("sampled %d hash prefixes", count)
	}
	before := Snapshot(math.MaxUint64, 1).Latest
	Record(Event{Hash: common.Hash{1}, Stage: "unsampled"})
	RecordAt(Event{Hash: common.Hash{0}, Stage: "missing"}, time.Time{})
	if after := Snapshot(math.MaxUint64, 1).Latest; after != before {
		t.Fatal("unsampled or missing-time evidence was written")
	}
	if allocations := testing.AllocsPerRun(100, func() { Record(Event{Hash: common.Hash{1}}) }); allocations != 0 {
		t.Fatalf("unsampled recording allocated %v objects", allocations)
	}
}

func TestBoundedPaginationAndReadOnlySnapshots(t *testing.T) {
	start := time.Now()
	r := newRecorder(3, start)
	for i := 1; i <= 5; i++ {
		r.recordAt(Event{Stage: "boundary"}, start.Add(time.Duration(i)*time.Microsecond))
	}
	p := r.snapshot(0, 2)
	if p.Oldest != 3 || p.Latest != 5 || p.Lost != 2 || p.Overwritten != 2 || p.Next != 4 || len(p.Events) != 2 {
		t.Fatalf("wrong retained page: %+v", p)
	}
	for i, event := range p.Events {
		if event.Sequence != uint64(i+3) || event.MonotonicUS != int64(i+3) {
			t.Fatalf("wrong event: %+v", event)
		}
	}
	last := r.snapshot(p.Next, 2)
	if last.Lost != 0 || len(last.Events) != 1 || last.Next != 5 {
		t.Fatalf("wrong continuation: %+v", last)
	}
	p.Events[0].Stage = "caller mutation"
	if again := r.snapshot(0, 2); again.Latest != 5 || again.Events[0].Stage != "boundary" {
		t.Fatal("reading or mutating a returned page altered the ring")
	}
	if p := r.snapshot(math.MaxUint64, 1); len(p.Events) != 0 {
		t.Fatal("future cursor overflowed")
	}
}

func TestConcurrentRingReadersAndWriters(t *testing.T) {
	r := newRecorder(64, time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				r.recordAt(Event{Stage: "complete"}, time.Now())
				p := r.snapshot(0, 13)
				for i, e := range p.Events {
					if e.Sequence != p.Oldest+uint64(i) || e.Stage != "complete" {
						t.Errorf("torn or unordered event: %+v", e)
					}
				}
			}
		}()
	}
	wg.Wait()
	if p := r.snapshot(0, MaxPage); p.Latest != 800 || len(p.Events) != 64 || p.Lost != 736 {
		t.Fatalf("concurrent ring not bounded: %+v", p)
	}
}

func TestCursorLossIsPerReadAndOverwrittenIsCumulative(t *testing.T) {
	start := time.Now()
	r := newRecorder(3, start)
	for i := 1; i <= 5; i++ {
		r.recordAt(Event{}, start.Add(time.Duration(i)*time.Microsecond))
	}
	first := r.snapshot(0, 1)
	if first.Next != 3 || first.Lost != 2 || first.Overwritten != 2 {
		t.Fatalf("wrong first overrun: %+v", first)
	}
	for i := 6; i <= 9; i++ {
		r.recordAt(Event{}, start.Add(time.Duration(i)*time.Microsecond))
	}
	overrun := r.snapshot(first.Next, 2)
	if overrun.Oldest != 7 || overrun.Next != 8 || overrun.Lost != 3 || overrun.Overwritten != 6 {
		t.Fatalf("wrong second overrun: %+v", overrun)
	}
	if again := r.snapshot(first.Next, 2); again.Lost != overrun.Lost || again.Overwritten != overrun.Overwritten {
		t.Fatal("re-reading changed loss counters")
	}
	drained := r.snapshot(overrun.Next, 2)
	if drained.Next != 9 || drained.Lost != 0 || drained.Overwritten != 6 {
		t.Fatalf("past eviction was counted as new capture loss: %+v", drained)
	}
	empty := r.snapshot(drained.Next, 2)
	if len(empty.Events) != 0 || empty.Next != 9 || empty.Lost != 0 {
		t.Fatalf("drained cursor did not remain stable: %+v", empty)
	}
}

func TestAttemptCorrelationAndOverlap(t *testing.T) {
	before := Snapshot(math.MaxUint64, 1).Latest
	hash := common.Hash{0, 0x91}
	a := NewAttempt(hash, "internal-sub", "rpc-sub", 77)
	b := NewAttempt(hash, "internal-sub", "rpc-sub", 78)
	if a.ID == b.ID {
		t.Fatal("simulation retries reused an attempt ID")
	}
	ctx := WithAttempt(context.Background(), a)
	if FromContext(ctx) != a || WithAttempt(ctx, nil) != ctx || NewAttempt(common.Hash{1}, "", "", 0) != nil {
		t.Fatal("wrong sampled context")
	}
	// Completion can be recorded later than a concurrent stage with an earlier
	// timestamp. Consumers must sort by time, not reinterpret event sequence.
	now := time.Now()
	a.Record("simulation_started", "", now)
	a.Record("subscriber_batch_dequeued", "", now.Add(-time.Millisecond))
	State(ctx, common.Hash{9}, "canonical_head")
	p := Snapshot(before, 10)
	if len(p.Events) != 3 || p.Events[0].Attempt != a.ID || p.Events[1].MonotonicUS >= p.Events[0].MonotonicUS || p.Events[2].Parent != (common.Hash{9}) {
		t.Fatalf("correlation/overlap lost: %+v", p.Events)
	}
}

func BenchmarkRecord(b *testing.B) {
	for _, sampled := range []bool{false, true} {
		name, hash := "unsampled", common.Hash{1}
		if sampled {
			name, hash = "sampled", common.Hash{0}
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				Record(Event{Hash: hash, Stage: "boundary"})
			}
		})
	}
}

func BenchmarkSnapshot(b *testing.B) {
	r := newRecorder(Capacity, time.Now())
	for i := 0; i < Capacity; i++ {
		r.recordAt(Event{Stage: "boundary"}, time.Now())
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.snapshot(0, MaxPage)
	}
}
