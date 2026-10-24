package pendingtrace

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// ReadTiming contains no payload. BufferedAt is the latest read completion
// contributing to the pre-read buffer, an upper bound on its arrival time.
type ReadTiming struct {
	BufferedBytes int
	BufferedAt    time.Time
	FrameBytes    int
	FrameComplete bool
	IOBegin       time.Time
	IOEnd         time.Time
	FrameEnd      time.Time
	DecodeEnd     time.Time
}

// PacketMeta is immutable once handed to another goroutine. IDs are local to
// this boot and reveal neither peer identity nor network address.
type PacketMeta struct {
	Connection, Number, Previous uint64
	Protocol                     string
	Version                      uint
	Code                         uint64
	ReadBegin, ReadEnd           time.Time
	PreviousReadEnd              time.Time
	PreviousDispatchBegin        time.Time
	PreviousDispatchEnd          time.Time
	Read                         ReadTiming
	Outcome                      string
}

type packetStamp struct {
	stage, outcome string
	at             time.Time
}

// Packet keeps four concurrent handoff/handler stamps until a sampled hash
// selects the packet. Every 32nd packet is also selected, retaining failures
// before decoding. There is no growing per-peer history or unbounded buffer.
type Packet struct {
	ID     uint64
	Meta   PacketMeta
	mu     sync.Mutex
	on     atomic.Bool
	stamps [4]packetStamp
}

func NewPacket(meta PacketMeta) *Packet {
	p := &Packet{ID: NextID(), Meta: meta}
	if meta.Number%SampleEvery == 0 || meta.Outcome == "error" {
		p.selectPacket()
	}
	return p
}

// Select retains a slow handoff and its previous protocol-handler packet.
func (p *Packet) Select() {
	if p != nil {
		p.selectPacket()
	}
}

func (p *Packet) Event(e Event) Event {
	if p != nil {
		e.Connection, e.Packet, e.PacketNumber = p.Meta.Connection, p.ID, p.Meta.Number
		e.Previous, e.Protocol, e.Version, e.Code = p.Meta.Previous, p.Meta.Protocol, p.Meta.Version, p.Meta.Code
	}
	return e
}

func (p *Packet) LinkHash(hash common.Hash) {
	if p != nil && Sampled(hash) {
		p.selectPacket()
		Record(p.Event(Event{Hash: hash, Stage: "packet_hash"}))
	}
}

func (p *Packet) selectPacket() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.on.Load() {
		return
	}
	p.on.Store(true)
	m := p.Meta
	p.span(Event{Stage: "p2p_read_gap"}, m.PreviousReadEnd, m.ReadBegin)
	p.span(Event{Stage: "p2p_previous_dispatch", Source: "preceding_message"}, m.PreviousDispatchBegin, m.PreviousDispatchEnd)
	p.span(Event{Stage: "p2p_read", Outcome: m.Outcome, Buffered: m.Read.BufferedBytes, FrameBytes: m.Read.FrameBytes}, m.ReadBegin, m.ReadEnd)
	p.span(Event{Stage: "rlpx_io_window"}, m.Read.IOBegin, m.Read.IOEnd)
	if !m.Read.FrameEnd.IsZero() {
		RecordAt(p.Event(Event{Scope: "packet", Stage: "rlpx_frame_ready", Operation: p.ID}), m.Read.FrameEnd)
	}
	if !m.Read.DecodeEnd.IsZero() {
		RecordAt(p.Event(Event{Scope: "packet", Stage: "rlpx_decode_done", Operation: p.ID}), m.Read.DecodeEnd)
	}
	if m.Read.BufferedBytes > 0 {
		outcome := "partial_frame"
		if m.Read.FrameComplete && m.Read.BufferedBytes >= m.Read.FrameBytes {
			outcome = "complete_frame"
		}
		p.span(Event{Stage: "rlpx_prefetch_wait", Outcome: outcome, Buffered: m.Read.BufferedBytes, FrameBytes: m.Read.FrameBytes}, m.Read.BufferedAt, m.ReadBegin)
	}
	for _, s := range p.stamps {
		if !s.at.IsZero() {
			RecordAt(p.Event(Event{Scope: "packet", Operation: p.ID, Stage: s.stage, Outcome: s.outcome}), s.at)
		}
	}
}

// Mark is limited to the four lifecycle points that can race across the
// unbuffered handoff. Each point is called once by its owning goroutine.
func (p *Packet) Mark(stage, outcome string, at time.Time) {
	if p == nil {
		return
	}
	index := 0
	switch stage {
	case "p2p_handoff_begin":
		index = 0
	case "p2p_handoff_end":
		index = 1
	case "eth_handler_begin":
		index = 2
	case "eth_handler_end":
		index = 3
	default:
		panic("unknown packet lifecycle stage")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stamps[index] = packetStamp{stage, outcome, at}
	if p.on.Load() {
		RecordAt(p.Event(Event{Scope: "packet", Operation: p.ID, Stage: stage, Outcome: outcome}), at)
	}
}

func (p *Packet) span(e Event, begin, end time.Time) {
	e.Scope = "packet"
	if e.Operation == 0 {
		e.Operation = p.ID
	}
	Span(p.Event(e), begin, end)
}

// Span records a selected packet's synchronous work without retaining payloads.
// A slow-path caller may select the packet before publishing its completed span.
func (p *Packet) Span(e Event, begin, end time.Time) {
	if p != nil && p.on.Load() {
		p.span(e, begin, end)
	}
}
