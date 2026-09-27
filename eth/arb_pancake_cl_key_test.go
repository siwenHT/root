package eth

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
	"testing"
)

func pancakeKeyFixture() []byte {
	raw := make([]byte, 192)
	copy(raw[44:64], common.HexToAddress("0x55d398326f99059ff775485246999027b3197955").Bytes())
	copy(raw[108:128], pancakeCLManager.Bytes())
	raw[159] = 67
	raw[189] = 1
	return raw
}

func TestPancakeKeyIdentityAndSpacing(t *testing.T) {
	raw := pancakeKeyFixture()
	id := common.HexToHash("0xd37aa0f0d66ad670279f6b89325c88bdff17d0265144762fb01f54fca9779944")
	key, spacing, err := verifiedInfinityKey(pancakeCLManager, id, raw, 1)
	if err != nil || spacing != 1 || key.Fee != 67 {
		t.Fatalf("real key: %v %v %v", key, spacing, err)
	}
	for _, hint := range []int64{-1, 2, 32768} {
		if _, _, err := verifiedInfinityKey(pancakeCLManager, id, raw, hint); err == nil {
			t.Fatal("accepted false hint")
		}
	}
	if _, _, err := verifiedInfinityKey(pancakeCLManager, common.Hash{}, raw, 0); err == nil {
		t.Fatal("accepted hash mismatch")
	}
	for _, at := range []int{0, 127, 189} {
		changed := append([]byte(nil), raw...)
		changed[at] = 2
		if at == 189 {
			changed[at] = 0
		}
		if _, _, err := verifiedInfinityKey(pancakeCLManager, crypto.Keccak256Hash(changed), changed, 0); err == nil {
			t.Fatalf("accepted bad byte %d", at)
		}
	}
}

func TestPancakeProtocolFeeAndWireMetadata(t *testing.T) {
	key := &arb.InfinityPoolKey{Fee: 67}
	slot := &arb.InfinitySlot0{LPFee: 67, ProtocolFee: 32 | (32 << 12)}
	fee := resolveInfinityFee(key, slot)
	if !fee.Resolved || fee.Num != 99 {
		t.Fatalf("fee %+v", fee)
	}
	slot.ProtocolFee = 32 | (100 << 12)
	if resolveInfinityFee(key, slot).Resolved {
		t.Fatal("flattened directional fees")
	}
	raw := "0x" + common.Bytes2Hex(pancakeKeyFixture())
	wire := poolSnapshotWire(arb.PoolSnapshot{Kind: "infinity_cl", PoolKeyData: raw, ProtocolFee: "409632", LPFee: "67"})
	if wire["pool_key_data"] != raw || wire["protocol_fee"] != "409632" || wire["lp_fee"] != "67" {
		t.Fatal("metadata lost")
	}
	if _, ok := wire["effective_fee_num"]; ok {
		t.Fatal("invented common fee")
	}
	legacy := poolSnapshotWire(arb.PoolSnapshot{Kind: "infinity_cl"})
	if _, ok := legacy["pool_key_data"]; ok {
		t.Fatal("invented legacy key")
	}
	slot.ProtocolFee = 4001
	if resolveInfinityFee(key, slot).Resolved {
		t.Fatal("invalid protocol fee")
	}
	key.Hooks[19] = 1
	slot.ProtocolFee = 0
	if resolveInfinityFee(key, slot).Resolved {
		t.Fatal("unreviewed hook")
	}
}
