package fetcher

import (
	"math"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestPendingTimingSeparatesAnnouncementWaitAndRequestScheduling(t *testing.T) {
	clock := new(mclock.Simulated)
	base := time.Now()
	requested := make(chan struct{}, 1)
	f := NewTxFetcherForTests(func(common.Hash) bool { return false }, nil,
		func(string, []common.Hash) error { requested <- struct{}{}; return nil }, nil,
		clock, func() time.Time { return base.Add(time.Duration(clock.Now())) }, nil)
	step := make(chan struct{})
	f.step = step
	f.Start()
	defer f.Stop()
	hash := common.Hash{0, 0xa1}
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	if err := f.Notify("peer", []byte{types.LegacyTxType}, []uint32{100}, []common.Hash{hash}); err != nil {
		t.Fatal(err)
	}
	<-step
	clock.Run(199 * time.Millisecond)
	select {
	case <-requested:
		t.Fatal("instrumentation changed the 200 ms grace period")
	default:
	}
	clock.Run(time.Millisecond)
	<-step
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not run after grace period")
	}
	stages := make(map[string]pendingtrace.Event)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
			if e.Hash == hash {
				stages[e.Stage] = e
			}
		}
		if _, ok := stages["fetch_request_end"]; ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	begin, end := stages["fetch_wait_begin"], stages["fetch_wait_end"]
	if begin.WaitUS != 200000 || begin.SlackUS != 50000 || end.MonotonicUS-begin.MonotonicUS != 200000 {
		t.Fatalf("grace period not measured independently: %+v", stages)
	}
	queued, started, finished := stages["fetch_request_queued"], stages["fetch_request_begin"], stages["fetch_request_end"]
	if queued.Operation == 0 || queued.Operation != started.Operation || started.Operation != finished.Operation || finished.Outcome != "ok" {
		t.Fatalf("request scheduling lost correlation: %+v", stages)
	}
}
