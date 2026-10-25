package ethapi

import (
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/rpc"
)

func TestPendingTimingRPCIsBoundedAndReadOnly(t *testing.T) {
	server := rpc.NewServer()
	if err := server.RegisterName("debug", &DebugAPI{}); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	client := rpc.DialInProc(server)
	defer client.Close()
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	pendingtrace.Record(pendingtrace.Event{Hash: common.Hash{0, 0xd1}, Stage: "rpc_test_1"})
	pendingtrace.Record(pendingtrace.Event{Hash: common.Hash{0, 0xd2}, Stage: "rpc_test_2"})
	var page pendingtrace.Page
	if err := client.CallContext(context.Background(), &page, "debug_pendingTiming", "0x"+strconv.FormatUint(before, 16), "0x1"); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Next != before+1 || page.SampleEvery != 32 || page.Capacity != pendingtrace.Capacity || page.BootID == "" {
		t.Fatalf("wrong RPC page: %+v", page)
	}
	var again pendingtrace.Page
	if err := client.CallContext(context.Background(), &again, "debug_pendingTiming", "0x"+strconv.FormatUint(before, 16), "0xffffffffffffffff"); err != nil {
		t.Fatal(err)
	}
	if len(again.Events) != 2 || again.Latest != page.Latest || again.Events[0].Sequence != page.Events[0].Sequence {
		t.Fatalf("read mutated evidence or limit was not bounded: %+v", again)
	}
}

func TestPendingTimingRPCReheapPayload(t *testing.T) {
	server := rpc.NewServer()
	if err := server.RegisterName("debug", &DebugAPI{}); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	client := rpc.DialInProc(server)
	defer client.Close()
	before := pendingtrace.Snapshot(math.MaxUint64, 1)
	stats := &pendingtrace.ReheapStats{Version: 1, Rebuild: 32, SampleEvery: 32, Total: 5}
	stats.Stages[0].Comparisons = 7
	stats.Stages[0].ZeroFast = 7
	stats.Stages[0].TypePairs[0][2] = 7
	pendingtrace.Record(pendingtrace.Event{Scope: "reheap", Stage: "priced_reheap", Pool: 17, Operation: 19, ParentOp: 13, Reheap: stats})
	var page pendingtrace.Page
	if err := client.CallContext(context.Background(), &page, "debug_pendingTiming", "0x"+strconv.FormatUint(before.Latest, 16), "0x1"); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.ReheapRecorded != before.ReheapRecorded+1 || page.Schema != 2 {
		t.Fatalf("wrong additive page %+v", page)
	}
	e := page.Events[0]
	if e.ParentOp != 13 || e.Operation != 19 || e.Pool != 17 || e.Reheap == nil || e.Reheap.Version != 1 || e.Reheap.Rebuild != 32 || e.Reheap.Stages[0].ZeroFast != 7 || e.Reheap.Stages[0].TypePairs[0][2] != 7 {
		t.Fatalf("RPC dropped diagnostic fields %+v", e)
	}
}
