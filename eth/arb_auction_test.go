package eth

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/arb"
)

func auctionFixture() AuctionState {
	return AuctionState{ID: "0x" + strings.Repeat("11", 32), ContentDigest: "0x" + strings.Repeat("22", 32),
		States: map[string]map[string]string{"0x" + strings.Repeat("33", 20): {common.Hash{}.Hex(): common.HexToHash("0x99").Hex()}}}
}

func TestAuctionDigest(t *testing.T) {
	a := auctionFixture()
	d, err := a.digest()
	if err != nil {
		t.Fatal(err)
	}
	if d.Hex() != "0x9aeb254f4ac242deeb9ace862a9b49584624f632c8aaa0a81c7e9b3fb1cb8ae5" {
		t.Fatal("Rust/Go overlay vector mismatch")
	}
	other := a
	other.ID = "0x" + strings.Repeat("44", 32)
	changed, _ := other.digest()
	if changed == d {
		t.Fatal("ID not bound")
	}
	a.States["0x"+strings.Repeat("33", 20)][common.Hash{}.Hex()] = common.Hash{}.Hex()
	changed, _ = a.digest()
	if changed == d {
		t.Fatal("zero-valued slot not bound")
	}
	if auctionRequestDigest(common.Hash{}.Hex(), d) == auctionRequestDigest(common.Hash{}.Hex(), changed) {
		t.Fatal("request not bound")
	}
	a.ID = "0x00"
	if _, err = a.digest(); err == nil {
		t.Fatal("short ID accepted")
	}
}

// Overlay writes must be visible to both unsigned and signed execution while
// preserving omitted slots, account metadata, the parent and nonce validation.
func TestAuctionOverlayExecutionIsolation(t *testing.T) {
	x, signer, bc := prefixFixture(t)
	defer bc.Stop()
	a := auctionFixture()
	pool := common.HexToAddress("0x" + strings.Repeat("33", 20))
	old := common.HexToHash("0x55")
	untouched := common.HexToHash("0x01")
	x.base.SetCode(pool, slotGetterSetterCode, tracing.CodeChangeUnspecified)
	x.base.SetState(pool, common.Hash{}, old)
	x.base.SetState(pool, untouched, old)
	x.base.Finalise(true)
	work := x.base.Copy()
	a.apply(work)
	if work.GetState(pool, common.Hash{}) != common.HexToHash("0x99") || work.GetState(pool, untouched) != old {
		t.Fatal("diff replaced full storage")
	}
	if x.base.GetState(pool, common.Hash{}) != old {
		t.Fatal("parent mutated")
	}
	pc := newPoolCallerOver(bc, work, x.parent, newReadBudget(1_000_000, 0))
	ret, err := pc.staticCall(pool, nil)
	if err != nil || new(big.Int).SetBytes(ret).Cmp(big.NewInt(153)) != 0 {
		t.Fatalf("overlay read: %x %v", ret, err)
	}
	overlay := newTargetExecutor(bc, x.parent, work)
	candidate := candidateEnvForKey1(0, pool, big.NewInt(0), x.baseFee())
	candidate.GasLimit = 100000
	unsigned, err := overlay.RunCandidate(nil, candidate, PurposeTradeCandidate, newReadBudget(1_000_000, 0))
	if err != nil || unsigned.Candidate == nil || unsigned.Candidate.Class.Status != arb.StatusSuccess {
		t.Fatalf("candidate: %v %v", unsigned, err)
	}
	candidate.Nonce = 99
	invalid, err := overlay.RunCandidate(nil, candidate, PurposeTradeCandidate, newReadBudget(1_000_000, 0))
	if err != nil || invalid.Candidate == nil || invalid.Candidate.Class.Status != arb.StatusInvalid {
		t.Fatal("overlay bypassed nonce")
	}
	to := common.BytesToAddress([]byte("recipient"))
	signed := mustSignValueTx(t, signer, 0, to, big.NewInt(1), x.baseFee())
	result, err := overlay.ExecutePrefix([]*types.Transaction{signed}, newReadBudget(1_000_000, 0))
	if err != nil || !result.Completed {
		t.Fatalf("signed: %v", err)
	}
	if result.PostState.GetState(pool, common.Hash{}) != common.HexToHash("0x99") {
		t.Fatal("signed path lost overlay")
	}
	a.States[pool.Hex()] = map[string]string{common.Hash{}.Hex(): common.Hash{}.Hex()}
	a.apply(work)
	if work.GetState(pool, common.Hash{}) != (common.Hash{}) || work.GetState(pool, untouched) != old {
		t.Fatal("zero diff semantics")
	}
	canonical, err := bc.StateAt(x.parent.Root)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.GetState(pool, common.Hash{}) != (common.Hash{}) {
		t.Fatal("canonical state mutated")
	}
}
