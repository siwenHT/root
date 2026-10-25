package pendingtrace

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReheapRecorderOwnershipAndLoss(t *testing.T) {
	r := newRecorder(2, time.Now())
	payload := &ReheapStats{Version: 1, Rebuild: 32, Total: 10}
	payload.Stages[0].TypePairs[0][2] = 17
	r.recordAt(Event{Scope: "reheap", Stage: "priced_reheap", Reheap: payload}, time.Now())
	payload.Total = 999
	payload.Stages[0].TypePairs[0][2] = 999
	p := r.snapshot(0, 2)
	if p.ReheapRecorded != 1 || p.ReheapOverwritten != 0 || p.Events[0].Reheap.Total != 10 || p.Events[0].Reheap.Stages[0].TypePairs[0][2] != 17 {
		t.Fatalf("recorder retained mutable input: %+v", p)
	}
	p.Events[0].Reheap.Stages[0].TypePairs[0][2] = 999
	if again := r.snapshot(0, 2); again.Events[0].Reheap.Stages[0].TypePairs[0][2] != 17 {
		t.Fatal("snapshot mutation escaped")
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Page
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Events[0].Reheap.Rebuild != 32 {
		t.Fatalf("wire payload lost: %s %v", data, err)
	}
	r.recordAt(Event{}, time.Now())
	r.recordAt(Event{}, time.Now())
	dropped := r.snapshot(0, 2)
	if dropped.ReheapRecorded != 1 || dropped.ReheapOverwritten != 1 || dropped.Lost != 1 {
		t.Fatalf("wrong eviction counters %+v", dropped)
	}
	if again := r.snapshot(dropped.Next, 2); again.ReheapOverwritten != 1 || again.Lost != 0 {
		t.Fatal("cumulative overwrite was confused with per-read loss")
	}
}
