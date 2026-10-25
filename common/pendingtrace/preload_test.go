package pendingtrace

import (
	"testing"
	"time"
)

func TestPreloadPayloadIsolation(t *testing.T) {
	r := newRecorder(8, time.Now())
	stats := &PreloadStats{Accounts: 42, Loaded: 42}
	r.recordAt(Event{Scope: "preload", Preload: stats}, time.Now())
	stats.Accounts = 7
	first := r.snapshot(0, 1)
	if first.Events[0].Preload.Accounts != 42 {
		t.Fatal("record retained mutable caller payload")
	}
	first.Events[0].Preload.Accounts = 8
	if r.snapshot(0, 1).Events[0].Preload.Accounts != 42 {
		t.Fatal("snapshot leaked mutable ring payload")
	}
}
