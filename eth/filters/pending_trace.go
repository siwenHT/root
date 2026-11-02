package filters

import (
	"time"

	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/types"
)

func tracePendingBatch(txs []*types.Transaction, stage, subscription string, batch uint64, at time.Time) {
	for _, tx := range txs {
		pendingtrace.RecordAt(pendingtrace.Event{Hash: tx.Hash(), Stage: stage, Subscription: subscription, Operation: batch}, at)
	}
}

func pendingOutcome(receipt *types.Receipt, err error) string {
	if err != nil {
		return "error"
	}
	if receipt == nil {
		return "no_receipt"
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return "reverted"
	}
	return "ok"
}
