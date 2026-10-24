package rlpx

import (
	"bytes"
	"net"
	"testing"
	"time"
)

type timingWireCapture struct {
	net.Conn
	wire bytes.Buffer
}

func (c *timingWireCapture) Write(b []byte) (int, error) { return c.wire.Write(b) }

func TestPendingTimingPrefetchedFrameNeedsNoNewSocketRead(t *testing.T) {
	reader, writer := createPeers(t)
	defer reader.Close()
	defer writer.Close()
	wire := &timingWireCapture{Conn: writer.conn}
	writer.conn = wire
	for _, payload := range []string{"first", "second"} {
		if _, err := writer.Write(8, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	reader.session.rbuf.reset()
	if reader.session.rbuf.end != 0 {
		t.Fatal("unexpected unread handshake bytes")
	}
	reader.session.rbuf.data = make([]byte, 0, 4096)
	sent := make(chan error, 1)
	go func() { _, err := wire.Conn.Write(wire.wire.Bytes()); sent <- err }()
	_, body, _, err := reader.Read()
	if err != nil || string(body) != "first" {
		t.Fatalf("first frame: %q %v", body, err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	time.Sleep(15 * time.Millisecond)
	_, body, _, err = reader.Read()
	timing := reader.ReadTiming()
	if err != nil || string(body) != "second" || !timing.FrameComplete || timing.BufferedBytes < timing.FrameBytes || timing.BufferedAt.After(at) || !timing.IOBegin.IsZero() || !timing.IOEnd.IsZero() {
		t.Fatalf("prefetched bytes were relabelled as a fresh socket read: %q %v %+v", body, err, timing)
	}
}
