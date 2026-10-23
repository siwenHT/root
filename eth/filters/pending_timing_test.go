package filters

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
)

func sampledPendingTx(n uint64) *types.Transaction {
	for {
		tx := pendingPipelineTx(n)
		if pendingtrace.Sampled(tx.Hash()) {
			return tx
		}
		n++
	}
}

func timingEvents(after uint64, hash common.Hash) []pendingtrace.Event {
	var out []pendingtrace.Event
	for _, e := range pendingtrace.Snapshot(after, pendingtrace.MaxPage).Events {
		if e.Hash == hash {
			out = append(out, e)
		}
	}
	return out
}

func waitTimingStage(t *testing.T, after uint64, hash common.Hash, stage string) []pendingtrace.Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		events := timingEvents(after, hash)
		for _, e := range events {
			if e.Stage == stage {
				return events
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing completed stage %s", stage)
	return nil
}

func TestPendingTimingIncludesPreBatchBufferWait(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 32)
	b, notifications, _ := pendingPipelineClient(t, func(ctx context.Context, tx *types.Transaction) (*types.Receipt, error) {
		started <- struct{}{}
		select {
		case <-release:
			return &types.Receipt{Status: types.ReceiptStatusSuccessful}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	var first []*types.Transaction
	for n := uint64(1); n <= 25; n++ {
		first = append(first, pendingPipelineTx(n))
	}
	b.txFeed.Send(core.NewTxsEvent{Txs: first})
	for n := 0; n < 24; n++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not fill")
		}
	}
	target := sampledPendingTx(10000)
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	b.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{target}})
	deadline := time.Now().Add(3 * time.Second)
	var send pendingtrace.Event
	for time.Now().Before(deadline) {
		for _, e := range timingEvents(before, target.Hash()) {
			if e.Stage == "subscriber_send_begin" {
				send = e
			}
			if e.Stage == "subscriber_batch_dequeued" {
				t.Fatal("second batch consumed while the first still had unscheduled work")
			}
		}
		if send.Sequence != 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if send.Sequence == 0 {
		t.Fatal("subscriber send was not recorded")
	}
	time.Sleep(25 * time.Millisecond)
	close(release)
	for {
		select {
		case hash := <-notifications:
			if hash == target.Hash() {
				goto delivered
			}
		case <-time.After(3 * time.Second):
			t.Fatal("queued target was not delivered")
		}
	}
delivered:
	stages := make(map[string]pendingtrace.Event)
	for _, e := range waitTimingStage(t, before, target.Hash(), "codec_write_end") {
		stages[e.Stage] = e
	}
	batch, worker := stages["subscriber_batch_dequeued"], stages["simulation_started"]
	if batch.MonotonicUS-send.MonotonicUS < 25000 || worker.MonotonicUS < batch.MonotonicUS {
		t.Fatalf("hidden pre-batch wait was omitted or merged with worker time: %+v", stages)
	}
	if stages["simulation_finished"].Outcome != "ok" || stages["codec_write_end"].Outcome != "ok" || worker.Subscription != send.Subscription || worker.Attempt == 0 {
		t.Fatalf("attempt lost across subscription/notification: %+v", stages)
	}
}

func TestPendingTimingRetainsRevertsAndDistinctSimulationAttempts(t *testing.T) {
	var calls atomic.Int32
	b, notifications, _ := pendingPipelineClient(t, func(context.Context, *types.Transaction) (*types.Receipt, error) {
		if calls.Add(1) == 1 {
			return &types.Receipt{Status: types.ReceiptStatusFailed}, nil
		}
		return &types.Receipt{Status: types.ReceiptStatusSuccessful}, nil
	})
	tx := sampledPendingTx(20000)
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	b.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{tx}})
	deadline := time.Now().Add(3 * time.Second)
	var firstID uint64
	for time.Now().Before(deadline) {
		for _, e := range timingEvents(before, tx.Hash()) {
			if e.Stage == "simulation_result_dequeued" && e.Outcome == "reverted" {
				firstID = e.Attempt
			}
		}
		if firstID != 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if firstID == 0 {
		t.Fatal("reverted simulation was not retained")
	}
	b.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{tx}})
	select {
	case hash := <-notifications:
		if hash != tx.Hash() {
			t.Fatal("wrong successful retry")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("successful retry was not delivered")
	}
	for _, e := range waitTimingStage(t, before, tx.Hash(), "codec_write_end") {
		if e.Stage == "codec_write_end" && e.Outcome == "ok" {
			if e.Attempt == firstID || e.Attempt == 0 {
				t.Fatal("node simulation retry reused its attempt ID")
			}
			return
		}
	}
	t.Fatal("successful retry write missing")
}

func TestPendingTimingSeparatesSubscribersAndRetainsCancellation(t *testing.T) {
	tx := sampledPendingTx(30000)
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	started := make(chan struct{}, 1)
	blocked, _, sub := pendingPipelineClient(t, func(ctx context.Context, _ *types.Transaction) (*types.Receipt, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	fast, notifications, _ := pendingPipelineClient(t, func(context.Context, *types.Transaction) (*types.Receipt, error) {
		return &types.Receipt{Status: types.ReceiptStatusSuccessful}, nil
	})
	blocked.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{tx}})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked subscription did not start")
	}
	fast.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{tx}})
	select {
	case <-notifications:
	case <-time.After(3 * time.Second):
		t.Fatal("independent subscription was blocked")
	}
	waitTimingStage(t, before, tx.Hash(), "codec_write_end")
	sub.Unsubscribe()
	events := waitTimingStage(t, before, tx.Hash(), "attempt_cancelled")
	subs := make(map[string]uint64)
	var cancelled, written uint64
	for _, e := range events {
		if e.Stage == "simulation_started" {
			subs[e.Subscription] = e.Attempt
		}
		if e.Stage == "attempt_cancelled" {
			cancelled = e.Attempt
		}
		if e.Stage == "codec_write_end" {
			written = e.Attempt
		}
	}
	if len(subs) != 2 || cancelled == 0 || written == 0 || cancelled == written {
		t.Fatalf("subscriber attempts or cancellation were merged: %+v", events)
	}
}
