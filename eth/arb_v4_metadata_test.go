package eth

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

func TestV4DirectionalMetadataDoesNotInventLegacyFee(t *testing.T) {
	raw := v4FeeKey(350)
	slot := &arb.InfinitySlot0{LPFee: 350, ProtocolFee: 93 | 94<<12}
	id := crypto.Keccak256Hash(raw)
	got := resolveV4StaticFee(id, raw, slot, 4)
	if got.Resolved || got.Num != 0 || !bytes.Equal(got.KeyData, raw) {
		t.Fatalf("directional metadata lost or flattened: %+v", got)
	}
	// Returned metadata owns its bytes, independent of caller buffer reuse.
	raw[31] ^= 1
	if crypto.Keccak256Hash(got.KeyData) != id {
		t.Fatal("key bytes alias read buffer")
	}
	wire := poolSnapshotWire(arb.PoolSnapshot{Kind: "v4", PoolKeyData: "0x" + common.Bytes2Hex(got.KeyData), ProtocolFee: "385117", LPFee: "350"})
	if wire["kind"] != "v4" || wire["lp_fee"] != "350" || wire["protocol_fee"] != "385117" {
		t.Fatal("metadata not retained on wire")
	}
	for _, at := range []int{0, 32, 159} {
		bad := append([]byte(nil), got.KeyData...)
		bad[at] = 1
		if len(resolveV4StaticFee(crypto.Keccak256Hash(bad), bad, slot, 4).KeyData) != 0 {
			t.Fatalf("invalid key at %d retained", at)
		}
	}
}

func TestV4PoolReadGateBindsReviewedManager(t *testing.T) {
	pr := PoolReadSpec{Kind: "v4", Locator: strings.ToLower(bscV4Manager.Hex()), Manager: strings.ToLower(bscV4Manager.Hex()),
		PoolKey: crypto.Keccak256Hash(v4FeeKey(350)).Hex(), Hook: (common.Address{}).Hex(),
		Token0: "0x0de81ca835ebbecdeed1dbc15be70bc134f0bf3b",
		Token1: "0x55d398326f99059ff775485246999027b3197955", TickSpacing: 4}
	if !validPoolRead(&pr) {
		t.Fatal("verified v4 read shape rejected")
	}
	for _, mutate := range []func(*PoolReadSpec){
		func(v *PoolReadSpec) { v.Manager = v.Token0; v.Locator = v.Token0 },
		func(v *PoolReadSpec) { v.Hook = v.Token0 },
		func(v *PoolReadSpec) { v.Token1 = "" },
		func(v *PoolReadSpec) { v.TickSpacing = 0 },
	} {
		bad := pr
		mutate(&bad)
		if validPoolRead(&bad) {
			t.Fatal("invalid v4 read shape accepted")
		}
	}
}
