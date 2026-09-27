package eth

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

var bscV4Manager = common.HexToAddress("0x28e2ea090877bf75740558f6bfb36a5ffee9e9df")
var bscV4Positions = common.HexToAddress("0x7a4a5c919ae2541aed11041a1aeee68f1287f95b")
var bscV4CodeHash = common.HexToHash("0x48752321ee7abf0d2a17c30679df9a1ddd14dc75d28b26e2509b76396145a005")

// The existing wire has one fee for both directions. Resolve it only when
// both directional protocol fees agree and the verified key has no hook.
// Dynamic or asymmetric fees remain unresolved, never flattened to an estimate.
func resolveV4StaticFee(id common.Hash, raw []byte, slot *arb.InfinitySlot0, spacing int64) InfinityFeeResolution {
	if len(raw) != 160 || crypto.Keccak256Hash(raw) != id {
		return unresolvedInfinityFee("v4_pool_key_mismatch")
	}
	for _, b := range raw[128:160] {
		if b != 0 {
			return unresolvedInfinityFee("v4_hook_required")
		}
	}
	for _, b := range append(append([]byte{}, raw[64:93]...), raw[96:125]...) {
		if b != 0 {
			return unresolvedInfinityFee("v4_fee_spacing_invalid")
		}
	}
	fee := binary.BigEndian.Uint32(raw[92:96])
	tickSpacing := binary.BigEndian.Uint32(raw[124:128])
	if fee >= infinityMaxLPFee || tickSpacing == 0 || int64(tickSpacing) != spacing || tickSpacing > 32767 {
		return unresolvedInfinityFee("v4_fee_spacing_invalid")
	}
	if slot == nil || slot.LPFee != fee {
		return unresolvedInfinityFee("v4_stored_fee_mismatch")
	}
	p0, p1 := slot.ProtocolFee&0xfff, slot.ProtocolFee>>12
	if p0 > 1000 || p1 > 1000 || p0 != p1 {
		return unresolvedInfinityFee("v4_directional_protocol_fee")
	}
	// Uniswap ProtocolFeeLibrary.calculateSwapFee, rounded as on chain.
	total := p0 + fee - uint32(uint64(p0)*uint64(fee)/1_000_000)
	return InfinityFeeResolution{Num: total, Den: infinityFeeDen, Resolved: true, Status: "v4_verified_static_slot"}
}

func (c *poolCaller) readV4StaticFee(manager common.Address, id common.Hash, slot *arb.InfinitySlot0, spacing int64) InfinityFeeResolution {
	if manager != bscV4Manager || c.wrapped.GetCodeHash(manager) != bscV4CodeHash {
		return unresolvedInfinityFee("pool_key_unavailable")
	}
	// poolKeys(bytes25) uses the first 25 bytes, LEFT aligned. Full hash
	// verification above prevents confusing keys sharing a shortened lookup.
	data := make([]byte, 36)
	copy(data[:4], []byte{0x86, 0xb6, 0xbe, 0x7d})
	copy(data[4:29], id[:25])
	raw, err := c.staticCall(bscV4Positions, data)
	if err != nil {
		return unresolvedInfinityFee("v4_pool_key_unavailable")
	}
	return resolveV4StaticFee(id, raw, slot, spacing)
}
