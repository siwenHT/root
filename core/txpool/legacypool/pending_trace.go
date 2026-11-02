package legacypool

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/types"
)

type pendingPoolTrace struct {
	hashes []common.Hash
	id     uint64
}

func tracePendingPool(txs []*types.Transaction) pendingPoolTrace {
	var trace pendingPoolTrace
	for _, tx := range txs {
		if hash := tx.Hash(); pendingtrace.Sampled(hash) {
			trace.hashes = append(trace.hashes, hash)
		}
	}
	if len(trace.hashes) > 0 {
		trace.id = pendingtrace.NextID()
	}
	return trace
}

func (t pendingPoolTrace) now() time.Time {
	if t.id == 0 {
		return time.Time{}
	}
	return time.Now()
}

func (t pendingPoolTrace) mark(stage string, at time.Time) {
	for _, hash := range t.hashes {
		pendingtrace.RecordAt(pendingtrace.Event{Hash: hash, Stage: stage, Operation: t.id, Source: "legacy_pool"}, at)
	}
}
