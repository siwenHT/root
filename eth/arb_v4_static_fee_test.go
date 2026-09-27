package eth

import (
	"encoding/binary"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

func v4FeeKey(fee uint32) []byte {
	raw := make([]byte, 160)
	copy(raw[12:32], common.HexToAddress("0x431a3bee82e2ca41e49895cbece5bb0f76a89b7a").Bytes())
	copy(raw[44:64], common.HexToAddress("0x55d398326f99059ff775485246999027b3197955").Bytes())
	binary.BigEndian.PutUint32(raw[92:96], fee)
	raw[127] = 4
	return raw
}

func TestV4StaticFeeCanonicalPoolAndProtocolRounding(t *testing.T) {
	raw := v4FeeKey(350)
	id := common.HexToHash("0x21632d9be3a5895d83a2f9adde60fa3b2450ffd596f897da1a048219b3963f99")
	if crypto.Keccak256Hash(raw) != id {
		t.Fatal("fixture does not match observed canonical Pool ID")
	}
	for _, tc := range []struct{ lp, protocol, want uint32 }{{350, 93, 443}, {3000, 500, 3499}, {100, 25, 125}, {0, 0, 0}} {
		raw = v4FeeKey(tc.lp)
		got := resolveV4StaticFee(crypto.Keccak256Hash(raw), raw, &arb.InfinitySlot0{LPFee: tc.lp, ProtocolFee: tc.protocol | tc.protocol<<12}, 4)
		if !got.Resolved || got.Num != tc.want || got.Den != 1_000_000 {
			t.Fatalf("%+v: %+v", tc, got)
		}
	}
}

func TestV4StaticFeeRejectsUnverifiedOrDirectionalState(t *testing.T) {
	raw := v4FeeKey(350)
	id := crypto.Keccak256Hash(raw)
	if resolveV4StaticFee(id, raw[:128], nil, 4).Resolved {
		t.Fatal("short key")
	}
	other := id
	other[31] ^= 1
	if resolveV4StaticFee(other, raw, &arb.InfinitySlot0{LPFee: 350}, 4).Resolved {
		t.Fatal("shortened ID collision")
	}
	for _, slot := range []*arb.InfinitySlot0{nil, {LPFee: 349}, {LPFee: 350, ProtocolFee: 93 | 94<<12}, {LPFee: 350, ProtocolFee: 1001 | 1001<<12}} {
		if resolveV4StaticFee(id, raw, slot, 4).Resolved {
			t.Fatalf("invalid slot accepted: %+v", slot)
		}
	}
	if resolveV4StaticFee(id, raw, &arb.InfinitySlot0{LPFee: 350}, 5).Resolved {
		t.Fatal("spacing mismatch")
	}
	raw[159] = 1
	if resolveV4StaticFee(crypto.Keccak256Hash(raw), raw, &arb.InfinitySlot0{LPFee: 350}, 4).Resolved {
		t.Fatal("hook fee guessed")
	}
	raw = v4FeeKey(0x800000)
	if resolveV4StaticFee(crypto.Keccak256Hash(raw), raw, &arb.InfinitySlot0{}, 4).Resolved {
		t.Fatal("dynamic fee guessed")
	}
}

func TestSingletonSpacingRangeAcrossWireAndStaticFee(t *testing.T) {
	for _, spacing := range []uint32{0, 1, 16383, 16384, 17600, 32767, 32768} {
		want := spacing > 0 && spacing <= 32767
		raw := v4FeeKey(350)
		binary.BigEndian.PutUint32(raw[124:128], spacing)
		id := crypto.Keccak256Hash(raw)
		manager := "0x28e2ea090877bf75740558f6bfb36a5ffee9e9df"
		pr := PoolReadSpec{Kind: "infinity_cl", Locator: manager, Manager: manager,
			PoolKey: id.Hex(), Hook: "0x0000000000000000000000000000000000000000",
			Token0: "0x431a3bee82e2ca41e49895cbece5bb0f76a89b7a", Token1: "0x55d398326f99059ff775485246999027b3197955", TickSpacing: int32(spacing)}
		if validPoolRead(&pr) != want {
			t.Fatalf("wire spacing %d: want %v", spacing, want)
		}
		got := resolveV4StaticFee(id, raw, &arb.InfinitySlot0{LPFee: 350}, int64(spacing))
		if got.Resolved != want {
			t.Fatalf("fee spacing %d: %+v", spacing, got)
		}
	}
}
