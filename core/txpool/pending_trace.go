package txpool

import (
	"time"

	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/types"
)

func traceAdd(txs []*types.Transaction, packet *pendingtrace.Packet) (uint64, func([]error)) {
	var sampled []int
	for i, tx := range txs {
		if pendingtrace.Sampled(tx.Hash()) {
			sampled = append(sampled, i)
		}
	}
	if len(sampled) == 0 && packet == nil {
		return 0, nil
	}
	id, begin := pendingtrace.NextID(), time.Now()
	for _, i := range sampled {
		pendingtrace.RecordAt(packet.Event(pendingtrace.Event{Hash: txs[i].Hash(), Stage: "pool_add_begin", Operation: id}), begin)
	}
	return id, func(errs []error) {
		end := time.Now()
		for _, i := range sampled {
			outcome := "accepted"
			if errs[i] != nil {
				outcome = "rejected"
			}
			pendingtrace.RecordAt(packet.Event(pendingtrace.Event{Hash: txs[i].Hash(), Stage: "pool_add_end", Operation: id, Outcome: outcome}), end)
		}
		packet.Span(pendingtrace.Event{Stage: "pool_add_call", Operation: id}, begin, end)
	}
}
