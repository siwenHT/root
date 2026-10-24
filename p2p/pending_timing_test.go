package p2p

import (
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
)

func TestPendingTimingBlockedProtocolDelaysNextRead(t *testing.T) {
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	first, second, third := make(chan Msg, 1), make(chan Msg, 1), make(chan Msg, 1)
	release := make(chan struct{})
	proto := Protocol{Name: "eth", Version: 68, Length: 17, Run: func(_ *Peer, rw MsgReadWriter) error {
		for i, ch := range []chan Msg{first, second, third} {
			msg, err := rw.ReadMsg()
			if err != nil {
				return err
			}
			msg.Discard()
			msg.Trace.LinkHash(common.Hash{0, byte(i + 0x81)})
			ch <- msg
			if i == 0 {
				<-release
			}
		}
		return nil
	}}
	closer, remote, _, _ := testPeer([]Protocol{proto})
	defer closer()
	secondWritten, thirdWritten := make(chan struct{}), make(chan struct{})
	go func() {
		Send(remote, baseProtocolLength+8, []uint{1})
		Send(remote, baseProtocolLength+8, []uint{2})
		close(secondWritten)
		Send(remote, baseProtocolLength+8, []uint{3})
		close(thirdWritten)
	}()
	var one Msg
	select {
	case one = <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("first packet not handled")
	}
	select {
	case <-secondWritten:
	case <-time.After(3 * time.Second):
		t.Fatal("second packet not read from transport")
	}
	select {
	case <-thirdWritten:
		t.Fatal("reader consumed the third packet while protocol handoff was blocked")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	var two, three Msg
	select {
	case two = <-second:
	case <-time.After(3 * time.Second):
		t.Fatal("second packet remained blocked")
	}
	select {
	case three = <-third:
	case <-time.After(3 * time.Second):
		t.Fatal("third packet not read after handoff")
	}
	if one.Trace.ID == two.Trace.ID || two.Trace.ID == three.Trace.ID || three.Trace.Meta.Previous != two.Trace.ID || one.Trace.Meta.Connection != three.Trace.Meta.Connection || three.Trace.Meta.Code != 8 || three.Trace.Meta.Protocol != "eth" {
		t.Fatal("connection, packet sequence or relative code was lost")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var begin, end, gap, blocker pendingtrace.Event
		for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
			if e.Packet == two.Trace.ID {
				switch e.Stage {
				case "p2p_handoff_begin":
					begin = e
				case "p2p_handoff_end":
					end = e
				case "p2p_handoff_previous_handler":
					blocker = e
				}
			}
			if e.Packet == three.Trace.ID && e.Stage == "p2p_read_gap" {
				gap = e
			}
		}
		if end.Sequence != 0 && gap.Sequence != 0 && blocker.Sequence != 0 {
			if end.MonotonicUS-begin.MonotonicUS < 20000 || gap.DurationUS < 20000 || blocker.ParentOp != one.Trace.ID {
				t.Fatalf("pre-read wait merged with network time: %+v %+v %+v", begin, end, gap)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("completed packet handoff or next-read gap missing")
}

func TestPendingTimingClosedHandoffHasTerminalOutcome(t *testing.T) {
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	proto := &protoRW{Protocol: Protocol{Name: "eth", Version: 68, Length: 17}, offset: baseProtocolLength, in: make(chan Msg)}
	peer := &Peer{running: map[string]*protoRW{"eth": proto}, closed: make(chan struct{})}
	now := time.Now()
	packet := pendingtrace.NewPacket(pendingtrace.PacketMeta{Connection: 9, Number: 32, ReadBegin: now, ReadEnd: now})
	done := make(chan error, 1)
	go func() { done <- peer.handle(Msg{Code: baseProtocolLength + 8, Trace: packet}) }()
	time.Sleep(5 * time.Millisecond)
	close(peer.closed)
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("close behavior changed: %v", err)
	}
	for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
		if e.Packet == packet.ID && e.Stage == "p2p_handoff_end" && e.Outcome == "closed" {
			return
		}
	}
	t.Fatal("closed handoff completion missing")
}
