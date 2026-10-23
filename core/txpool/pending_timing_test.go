package txpool

import (
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
)

type timingSubPool struct {
	SubPool
	accepted, rejected *types.Transaction
	err                error
}

func (p *timingSubPool) Filter(tx *types.Transaction) bool {
	return tx == p.accepted || tx == p.rejected
}

func (p *timingSubPool) Add(txs []*types.Transaction, _ bool) []error {
	errs := make([]error, len(txs))
	for i, tx := range txs {
		if tx == p.rejected {
			errs[i] = p.err
		}
	}
	return errs
}

func TestPendingTimingPreservesAdmissionErrorsAndOutcomes(t *testing.T) {
	var txs []*types.Transaction
	for nonce := uint64(0); len(txs) < 3; nonce++ {
		tx := types.NewTx(&types.LegacyTx{Nonce: nonce, Gas: 21000, GasPrice: big.NewInt(1)})
		if pendingtrace.Sampled(tx.Hash()) {
			txs = append(txs, tx)
		}
	}
	wantErr := errors.New("subpool rejection")
	subpool := &timingSubPool{accepted: txs[0], rejected: txs[1], err: wantErr}
	pool := &TxPool{subpools: []SubPool{subpool}}
	before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
	errs := pool.Add(txs, false)
	if len(errs) != 3 || errs[0] != nil || errs[1] != wantErr || !errors.Is(errs[2], core.ErrTxTypeNotSupported) {
		t.Fatalf("admission changed or reordered errors: %v", errs)
	}
	for i, tx := range txs {
		var begin, end pendingtrace.Event
		for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
			if e.Hash == tx.Hash() {
				switch e.Stage {
				case "pool_add_begin":
					begin = e
				case "pool_add_end":
					end = e
				}
			}
		}
		want := "rejected"
		if i == 0 {
			want = "accepted"
		}
		if begin.Sequence == 0 || end.Outcome != want || end.Operation != begin.Operation || end.MonotonicUS < begin.MonotonicUS {
			t.Fatalf("admission outcome lost for index %d: %+v %+v", i, begin, end)
		}
	}
}
