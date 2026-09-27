package eth

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostPoolReadersDoNotStrandWorkBehindSlowPool(t *testing.T) {
	const pools, workers = 32, 8
	blocked := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	finished := make(chan int, pools)
	done := make(chan []error, 1)
	var active [workers]atomic.Int32
	var concurrent atomic.Bool
	go func() {
		done <- runPostPoolReads(pools, workers, func(w, i int) error {
			if active[w].Add(1) != 1 {
				concurrent.Store(true)
			}
			defer active[w].Add(-1)
			if i == 0 {
				close(blocked)
				<-release
			} else {
				finished <- i
			}
			return nil
		})
	}()
	<-blocked
	seen := make(map[int]bool)
	for len(seen) < pools-1 {
		select {
		case i := <-finished:
			if i < 1 || i >= pools || seen[i] {
				t.Fatalf("duplicate/out-of-range pool index %d", i)
			}
			seen[i] = true
		case <-time.After(5 * time.Second):
			t.Fatal("idle readers left work behind the blocked pool")
		}
	}
	if concurrent.Load() {
		t.Fatal("one reader used concurrently")
	}
	// The batch must still wait for the blocked read, never expose a partial result.
	select {
	case <-done:
		t.Fatal("batch completed before all pools")
	default:
	}
}

func TestPostPoolReaderErrorRemainsVisible(t *testing.T) {
	want := errors.New("snapshot read failed")
	var calls atomic.Int32
	errs := runPostPoolReads(32, 8, func(w, i int) error {
		calls.Add(1)
		return want
	})
	if len(errs) != 8 || calls.Load() != 8 {
		t.Fatalf("failed readers must stop: errors=%d calls=%d", len(errs), calls.Load())
	}
	for _, err := range errs {
		if !errors.Is(err, want) {
			t.Fatalf("read error lost: %v", err)
		}
	}
}
