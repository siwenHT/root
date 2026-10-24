package pendingtrace

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func TestPendingPacketConcurrentSelectionAndHandoff(t *testing.T) {
	before := Snapshot(math.MaxUint64, 1).Latest
	now := time.Now()
	p := NewPacket(PacketMeta{Connection: 91, Number: 1, Protocol: "eth", Version: 68, Code: 8,
		ReadBegin: now.Add(-time.Millisecond), ReadEnd: now})
	p.Mark("p2p_handoff_begin", "", now)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.LinkHash(common.Hash{0, 0x71}) }()
	go func() { defer wg.Done(); p.Mark("p2p_handoff_end", "ok", now.Add(time.Millisecond)) }()
	wg.Wait()
	p.LinkHash(common.Hash{0, 0x72})
	counts := make(map[string]int)
	for _, e := range Snapshot(before, MaxPage).Events {
		if e.Packet != p.ID {
			continue
		}
		if e.Connection != 91 || e.PacketNumber != 1 || e.Protocol != "eth" || e.Version != 68 || e.Code != 8 {
			t.Fatalf("packet identity mixed: %+v", e)
		}
		counts[e.Stage]++
	}
	if counts["p2p_read"] != 1 || counts["p2p_handoff_begin"] != 1 || counts["p2p_handoff_end"] != 1 || counts["packet_hash"] != 2 {
		t.Fatalf("selection duplicated or lost lifecycle points: %v", counts)
	}
}

func TestPendingPacketFailureAndPrefetchAreNotRemoteLatency(t *testing.T) {
	before := Snapshot(math.MaxUint64, 1).Latest
	now := time.Now()
	p := NewPacket(PacketMeta{Connection: 92, Number: 32, Outcome: "error", ReadBegin: now, ReadEnd: now.Add(time.Millisecond),
		Read: ReadTiming{BufferedBytes: 96, BufferedAt: now.Add(-20 * time.Millisecond), FrameBytes: 96, FrameComplete: true}})
	stages := make(map[string]Event)
	for _, e := range Snapshot(before, MaxPage).Events {
		if e.Packet == p.ID {
			stages[e.Stage] = e
		}
	}
	if stages["p2p_read"].Outcome != "error" || stages["rlpx_prefetch_wait"].Outcome != "complete_frame" || stages["rlpx_prefetch_wait"].DurationUS != 20000 {
		t.Fatalf("pre-read local evidence or failure lost: %+v", stages)
	}
	if stages["rlpx_io_window"].Sequence != 0 {
		t.Fatal("missing socket read was fabricated")
	}
}

func BenchmarkPendingPacketUnselected(b *testing.B) {
	now := time.Now()
	meta := PacketMeta{Connection: 1, Number: 1, ReadBegin: now, ReadEnd: now}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p := NewPacket(meta)
		p.Mark("p2p_handoff_begin", "", now)
		p.Mark("p2p_handoff_end", "ok", now)
	}
}
