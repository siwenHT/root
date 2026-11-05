package eth

import (
	"bytes"
	"encoding/binary"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

var bscV4Manager = common.HexToAddress("0x28e2ea090877bf75740558f6bfb36a5ffee9e9df")
var bscV4Positions = common.HexToAddress("0x7a4a5c919ae2541aed11041a1aeee68f1287f95b")
var bscV4CodeHash = common.HexToHash("0x48752321ee7abf0d2a17c30679df9a1ddd14dc75d28b26e2509b76396145a005")

// Keep full PoolKey metadata for hooks and dynamic fees. The legacy scalar
// fee is resolved only for hook-free static pools with symmetric protocol fees.
func resolveV4StaticFee(id common.Hash, raw []byte, slot *arb.InfinitySlot0, spacing int64) InfinityFeeResolution {
	if len(raw) != 160 || crypto.Keccak256Hash(raw) != id {
		return unresolvedInfinityFee("v4_pool_key_mismatch")
	}
	for _, word := range [][]byte{raw[:32], raw[32:64]} {
		if !bytes.Equal(word[:12], make([]byte, 12)) {
			return unresolvedInfinityFee("v4_currency_padding")
		}
	}
	if bytes.Compare(raw[:32], raw[32:64]) >= 0 {
		return unresolvedInfinityFee("v4_currency_order")
	}
	if !bytes.Equal(raw[128:140], make([]byte, 12)) {
		return unresolvedInfinityFee("v4_hook_padding")
	}
	for _, b := range append(append([]byte{}, raw[64:92]...), raw[96:125]...) {
		if b != 0 {
			return unresolvedInfinityFee("v4_fee_spacing_invalid")
		}
	}
	fee := binary.BigEndian.Uint32(raw[92:96])
	tickSpacing := binary.BigEndian.Uint32(raw[124:128])
	dynamic := fee == infinityDynamicFeeFlag
	hook := common.BytesToAddress(raw[140:160])
	if (fee >= infinityMaxLPFee && !dynamic) || (dynamic && hook == (common.Address{})) || tickSpacing == 0 || int64(tickSpacing) != spacing || tickSpacing > 32767 {
		return unresolvedInfinityFee("v4_fee_spacing_invalid")
	}
	if slot == nil || slot.LPFee >= infinityMaxLPFee || (!dynamic && slot.LPFee != fee) {
		return unresolvedInfinityFee("v4_stored_fee_mismatch")
	}
	p0, p1 := slot.ProtocolFee&0xfff, slot.ProtocolFee>>12
	if p0 > 1000 || p1 > 1000 {
		return unresolvedInfinityFee("v4_directional_protocol_fee")
	}
	keyData := append([]byte(nil), raw...)
	if dynamic || hook != (common.Address{}) {
		return InfinityFeeResolution{KeyData: keyData, Status: "v4_hook_evm_required"}
	}
	if p0 != p1 {
		// Complete v4 metadata supports directional quotes; legacy scalar
		// consumers must still leave this fee unresolved.
		return InfinityFeeResolution{KeyData: keyData, Status: "v4_directional_protocol_fee"}
	}
	// Uniswap ProtocolFeeLibrary.calculateSwapFee, rounded as on chain.
	total := p0 + fee - uint32(uint64(p0)*uint64(fee)/1_000_000)
	return InfinityFeeResolution{KeyData: keyData, Num: total, Den: infinityFeeDen, Resolved: true, Status: "v4_verified_static_slot"}
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
