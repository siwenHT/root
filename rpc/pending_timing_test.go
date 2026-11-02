package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
)

func attemptEvents(after uint64, attempt uint64) []pendingtrace.Event {
	var events []pendingtrace.Event
	for _, e := range pendingtrace.Snapshot(after, pendingtrace.MaxPage).Events {
		if e.Attempt == attempt {
			events = append(events, e)
		}
	}
	return events
}

func TestPendingTimingBufferedNotifyIsNotAWrite(t *testing.T) {
	var out bytes.Buffer
	id := ID("timing-buffered")
	n := &Notifier{h: &handler{conn: &mockConn{json.NewEncoder(&out)}}, sub: &Subscription{ID: id}}
	a := pendingtrace.NewAttempt(common.Hash{0, 0x77}, "internal", string(id), 1)
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	if err := n.NotifyWithContext(pendingtrace.WithAttempt(context.Background(), a), id, "hello"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("inactive subscription wrote before activation")
	}
	for _, e := range attemptEvents(before, a.ID) {
		if e.Stage == "codec_write_begin" || e.Stage == "codec_write_end" {
			t.Fatal("buffering was reported as socket output")
		}
	}
	if err := n.activate(); err != nil {
		t.Fatal(err)
	}
	want := "{\"jsonrpc\":\"2.0\",\"method\":\"_subscription\",\"params\":{\"subscription\":\"timing-buffered\",\"result\":\"hello\"}}\n"
	if out.String() != want {
		t.Fatalf("notification payload changed: %s", out.String())
	}
	stages := make(map[string]pendingtrace.Event)
	for _, e := range attemptEvents(before, a.ID) {
		stages[e.Stage] = e
	}
	if stages["notify_return"].Sequence >= stages["codec_write_begin"].Sequence || stages["codec_write_end"].Outcome != "ok" {
		t.Fatalf("activation lost the original attempt or write boundary: %+v", stages)
	}
}

type blockedTimingConn struct {
	*mockConn
	started chan struct{}
	release chan struct{}
	err     error
}

func (c *blockedTimingConn) writeJSON(ctx context.Context, v interface{}, isError bool) error {
	if ctx.Err() != nil {
		return errors.New("diagnostic context changed transport cancellation")
	}
	close(c.started)
	<-c.release
	return c.err
}

func TestPendingTimingWriteBlocksAndPreservesErrors(t *testing.T) {
	wantErr := errors.New("write failed")
	conn := &blockedTimingConn{started: make(chan struct{}), release: make(chan struct{}), err: wantErr}
	id := ID("timing-blocked")
	n := &Notifier{h: &handler{conn: conn}, sub: &Subscription{ID: id}, activated: true}
	a := pendingtrace.NewAttempt(common.Hash{0, 0x78}, "internal", string(id), 2)
	ctx, cancel := context.WithCancel(pendingtrace.WithAttempt(context.Background(), a))
	cancel()
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	done := make(chan error, 1)
	go func() { done <- n.NotifyWithContext(ctx, id, "same-payload") }()
	select {
	case <-conn.started:
	case <-time.After(3 * time.Second):
		t.Fatal("write did not begin")
	}
	for _, e := range attemptEvents(before, a.ID) {
		if e.Stage == "codec_write_end" || e.Stage == "notify_return" {
			t.Fatal("blocked write was reported complete")
		}
	}
	close(conn.release)
	if err := <-done; err != wantErr {
		t.Fatalf("transport error changed: %v", err)
	}
	stages := make(map[string]string)
	for _, e := range attemptEvents(before, a.ID) {
		stages[e.Stage] = e.Outcome
	}
	if stages["codec_write_end"] != "error" || stages["notify_return"] != "error" {
		t.Fatalf("write failure disappeared from trace: %+v", stages)
	}
}
