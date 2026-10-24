package fetcher

import (
	"math"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestPendingPacketRejectionBackoffSeparateFromGrace(t *testing.T) {
	packet := pendingtrace.NewPacket(pendingtrace.PacketMeta{Connection: 17, Number: 1})
	f := NewTxFetcherWithTrace(func(common.Hash) bool { return false }, func(_ string, txs []*types.Transaction, p *pendingtrace.Packet) []error {
		if p != packet {
			t.Error("admission callback lost packet")
		}
		errs := make([]error, len(txs))
		for i := range errs {
			errs[i] = core.ErrNonceTooLow
		}
		return errs
	}, nil, nil)
	defer f.Stop()
	txs := make([]*types.Transaction, 33)
	for i := range txs {
		txs[i] = types.NewTx(&types.LegacyTx{Nonce: uint64(i)})
	}
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	done := make(chan error, 1)
	go func() { done <- f.EnqueueWithTrace("peer", txs, false, packet) }()
	select {
	case <-f.cleanup:
	case <-time.After(3 * time.Second):
		t.Fatal("backoff/cleanup never completed")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var backoff, add pendingtrace.Event
	for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
		if e.Packet != packet.ID {
			continue
		}
		if e.Stage == "fetch_wait_begin" || e.Stage == "fetch_wait_end" {
			t.Fatal("rejection sleep relabelled as announcement grace")
		}
		if e.Stage == "fetch_rejection_backoff" {
			backoff = e
		}
		if e.Stage == "fetch_admission" {
			add = e
		}
	}
	if backoff.DurationUS < 190000 || backoff.Rejected != 33 || backoff.BatchSize != 33 || backoff.Operation == 0 || backoff.Operation != add.Operation {
		t.Fatalf("200ms rejection branch lost its batch/cause: %+v %+v", backoff, add)
	}
}

func TestPendingPacketFetcherCancellationIsRetained(t *testing.T) {
	packet := pendingtrace.NewPacket(pendingtrace.PacketMeta{Connection: 18, Number: 32})
	f := NewTxFetcher(func(common.Hash) bool { return false }, func(_ string, txs []*types.Transaction) []error { return make([]error, len(txs)) }, nil, nil)
	f.Stop()
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	if err := f.NotifyWithTrace("peer", []byte{0}, []uint32{100}, []common.Hash{{0, 0x83}}, packet); err != errTerminated {
		t.Fatal(err)
	}
	if err := f.EnqueueWithTrace("peer", []*types.Transaction{types.NewTx(&types.LegacyTx{})}, false, packet); err != errTerminated {
		t.Fatal(err)
	}
	stages := make(map[string]string)
	for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
		if e.Packet == packet.ID {
			stages[e.Stage] = e.Outcome
		}
	}
	if stages["fetch_notify_handoff"] != "cancelled" || stages["fetch_cleanup_handoff"] != "cancelled" {
		t.Fatalf("cancelled handoff missing: %v", stages)
	}
}
