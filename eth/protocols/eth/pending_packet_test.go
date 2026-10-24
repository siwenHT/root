package eth

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/p2p"
)

type timingMessageReader struct {
	p2p.MsgReadWriter
	msg p2p.Msg
}

func (r *timingMessageReader) ReadMsg() (p2p.Msg, error) { return r.msg, nil }

type timingMessageBackend struct{ Backend }

func (timingMessageBackend) AcceptTxs() bool { return true }

func TestPendingPacketETHFailureClosesHandler(t *testing.T) {
	for _, test := range []struct {
		name string
		code uint64
		size uint32
	}{{"size", 8, maxMessageSize + 1}, {"code", 99, 1}, {"decode", 8, 1}} {
		t.Run(test.name, func(t *testing.T) {
			before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
			now := time.Now()
			packet := pendingtrace.NewPacket(pendingtrace.PacketMeta{Connection: 71, Number: 32, ReadBegin: now, ReadEnd: now})
			peer := &Peer{version: ETH68, rw: &timingMessageReader{msg: p2p.Msg{Code: test.code, Size: test.size, Payload: bytes.NewReader([]byte{0xff}), Trace: packet}}}
			if err := handleMessage(timingMessageBackend{}, peer); err == nil {
				t.Fatal("invalid message accepted")
			}
			for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
				if e.Packet == packet.ID && e.Stage == "eth_handler_end" && e.Outcome == "error" {
					return
				}
			}
			t.Fatal("failed handler has no completion")
		})
	}
}
