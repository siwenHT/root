package fetcher

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestPendingArrivalTimingConfiguration(t *testing.T) {
	for _, tc := range []struct {
		raw         string
		wait, slack time.Duration
	}{
		{"", 200 * time.Millisecond, 50 * time.Millisecond},
		{"25", 25 * time.Millisecond, 6250 * time.Microsecond},
		{"1", time.Millisecond, 250 * time.Microsecond},
		{"200", 200 * time.Millisecond, 50 * time.Millisecond},
		{"2000", 2 * time.Second, 50 * time.Millisecond},
		{"0", 200 * time.Millisecond, 50 * time.Millisecond},
		{"-1", 200 * time.Millisecond, 50 * time.Millisecond},
		{"2001", 200 * time.Millisecond, 50 * time.Millisecond},
		{"bad", 200 * time.Millisecond, 50 * time.Millisecond},
	} {
		wait, slack := pendingArrivalTiming(tc.raw)
		if wait != tc.wait || slack != tc.slack {
			t.Fatalf("%q: got %v/%v want %v/%v", tc.raw, wait, slack, tc.wait, tc.slack)
		}
	}
	t.Setenv("ARB_TX_ARRIVE_TIMEOUT_MS", "25")
	f := NewTxFetcher(nil, nil, nil, nil)
	if f.arriveTimeout != 25*time.Millisecond || f.arriveSlack != 6250*time.Microsecond {
		t.Fatal("production constructor ignored configured timing")
	}
}

func TestPendingArrivalShortWaitRequestsAtConfiguredDeadline(t *testing.T) {
	testTransactionFetcherParallel(t, txFetcherTest{
		init: func() *TxFetcher {
			f := NewTxFetcher(func(common.Hash) bool { return false }, nil, func(string, []common.Hash) error { return nil }, nil)
			f.arriveTimeout, f.arriveSlack = pendingArrivalTiming("25")
			return f
		},
		steps: []interface{}{
			doTxNotify{peer: "A", hashes: []common.Hash{{1}}, types: []byte{types.LegacyTxType}, sizes: []uint32{111}},
			doWait{time: 24 * time.Millisecond},
			isWaiting(map[string][]announce{"A": {{common.Hash{1}, types.LegacyTxType, 111}}}),
			isScheduled{tracking: nil, fetching: nil},
			doWait{time: time.Millisecond, step: true},
			isWaiting(nil),
			isScheduled{tracking: map[string][]announce{"A": {{common.Hash{1}, types.LegacyTxType, 111}}}, fetching: map[string][]common.Hash{"A": {{1}}}},
		},
	})
}
