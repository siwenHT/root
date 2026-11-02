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
