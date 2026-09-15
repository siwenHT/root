// Fixed-parent pool state reader (NODE-02.2, design §9.3/§9.5, §6.1). It binds the
// pure ABI codec (eth/arb/pool_abi.go) and the budgeted state wrapper
// (eth/arb_budget_statedb.go) onto a real EVM over a FIXED parent state, and reads
// pool state by read-only view calls. It lives in package eth so it can touch the
// node's EVM, blockchain, and state types.
//
// Read-only guarantee (design §6.1 "模拟禁止调用Commit/向canonical TrieDB写入"):
// every pool accessor is issued via evm.StaticCall, which forbids ALL state writes
// at the EVM level — the strongest honest read-only guarantee, stronger than merely
// not calling Commit. We also run against a StateDB.Copy of the borrowed base so
// even lazy cache fills never touch the canonical/base object shared elsewhere.
//
// Honesty (design §381/§9.3):
//   - a malformed/short view-call return is a hard error (from the ABI codec); we
//     never fabricate a zero reserve/price.
//   - V2 reserves are only trusted for standard-math quoting when the pool's real
//     token balances equal the reserves (§9.3); we read balanceOf for both tokens
//     and let the caller/Rust gate on balances_match_reserves. We report balances
//     as read; we do not overwrite reserves with balances.
//   - if the read budget trips mid-read, the wrapper cancels and we surface the
//     budget error; the partial result is discarded, never returned as success.
package eth

import (
	"encoding/binary"
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

// poolCaller wraps a budgeted EVM over a fixed parent state for issuing read-only
// pool view calls. Build it once per snapshot batch; reuse across pools sharing the
// same parent to amortize the state Copy and block context.
type poolCaller struct {
	evm     *vm.EVM
	wrapped *budgetedStateDB
	caller  common.Address // an arbitrary EOA origin for the static calls
	gasCap  uint64
}

// viewGas bounds a single view call. Pool accessors are cheap; this only guards a
// pathological contract, and the read budget is the real limiter.
const viewGas = 2_000_000

// PoolReadError wraps a budget/exec failure so callers can distinguish it from a
// clean "pool does not implement this" decode error.
var (
	ErrViewCallReverted  = errors.New("arb: pool view call reverted or failed")
	ErrReadBudgetTripped = errors.New("arb: read budget tripped during pool read")
)

// newPoolCallerOver builds a caller over a Copy of base at the given parent header,
// depending only on core.ChainContext (config + engine + header lookups) so it works
// over EITHER the canonical head state OR an executor's isolated post-target state
// (the §256 quotable post handle) — the NODE-03→NODE-04 bridge. The author is resolved
// explicitly (design §7.1: never let a failed Author extraction become the zero
// address); for a read-only snapshot the coinbase does not affect pool getters, but we
// still bind it honestly from the header's real author when available.
func newPoolCallerOver(chain core.ChainContext, base *state.StateDB, parent *types.Header, budget *arb.ReadBudget) *poolCaller {
	// Work on a private copy so lazy cache fills never mutate the borrowed base
	// (design §6.1: Copy shares underlying readers; we still isolate the mutable
	// in-memory objects, and StaticCall prevents writes regardless).
	work := base.Copy()
	wrapped := newBudgetedStateDB(work, budget)

	// Explicit author: prefer the consensus engine's author for the (already
	// validated) parent header; fall back to the header coinbase. Never zero via a
	// silently-ignored error.
	author := parent.Coinbase
	if a, err := chain.Engine().Author(parent); err == nil {
		author = a
	}

	blockCtx := core.NewEVMBlockContext(parent, chain, &author)
	evm := vm.NewEVM(blockCtx, wrapped, chain.Config(), vm.Config{})
	wrapped.SetCancel(evm.Cancel)

	return &poolCaller{
		evm:     evm,
		wrapped: wrapped,
		caller:  common.BytesToAddress([]byte("arb-pool-reader")),
		gasCap:  viewGas,
	}
}

// newPoolCaller is the *Ethereum convenience wrapper over newPoolCallerOver, binding
// the node's own blockchain as the chain context.
func (s *Ethereum) newPoolCaller(base *state.StateDB, parent *types.Header, budget *arb.ReadBudget) *poolCaller {
	return newPoolCallerOver(s.blockchain, base, parent, budget)
}

// staticCall issues one read-only call to addr with calldata, returning the raw
// return bytes. It fails if the budget tripped (cancel latched) or the call errored
// (revert / out-of-gas). It NEVER returns a fabricated value on failure.
func (c *poolCaller) staticCall(addr common.Address, calldata []byte) ([]byte, error) {
	ret, _, err := c.evm.StaticCall(c.caller, addr, calldata, c.gasCap)
	if c.wrapped.Budget().Failed() {
		return nil, ErrReadBudgetTripped
	}
	if err != nil {
		return nil, ErrViewCallReverted
	}
	return ret, nil
}

// V2Snapshot is the read result for a V2 pool: reserves plus the real token
// balances (design §9.3). The caller maps these onto arb-types V2State and gates
// quoting on balances == reserves.
type V2Snapshot struct {
	Reserve0 *big.Int
	Reserve1 *big.Int
	Balance0 *big.Int
	Balance1 *big.Int
}

// ReadV2 reads getReserves() on the pool and balanceOf(pool) on both tokens. token0
// and token1 are the pool's token addresses from the registry (not re-read here;
// the registry is the identity authority). Any decode/budget/revert failure aborts
// with an error — no partial or fabricated snapshot.
func (c *poolCaller) ReadV2(pool, token0, token1 common.Address) (*V2Snapshot, error) {
	ret, err := c.staticCall(pool, arb.CallGetReserves())
	if err != nil {
		return nil, err
	}
	res, err := arb.DecodeGetReserves(ret)
	if err != nil {
		return nil, err
	}

	bal := func(token common.Address) (*big.Int, error) {
		var raw [20]byte
		copy(raw[:], pool.Bytes())
		out, err := c.staticCall(token, arb.CallBalanceOf(raw))
		if err != nil {
			return nil, err
		}
		return arb.DecodeUint256(out)
	}
	b0, err := bal(token0)
	if err != nil {
		return nil, err
	}
	b1, err := bal(token1)
	if err != nil {
		return nil, err
	}

	return &V2Snapshot{
		Reserve0: res.Reserve0,
		Reserve1: res.Reserve1,
		Balance0: b0,
		Balance1: b1,
	}, nil
}

// V3Snapshot is the read result for a V3 pool head: slot0 (sqrtPriceX96, tick) and
// liquidity. Bitmap words and ticks are read on demand elsewhere (design §9.5); the
// head is what the identity_only + on-demand model needs first.
type V3Snapshot struct {
	SqrtPriceX96 *big.Int
	Tick         int32
	Liquidity    *big.Int
}

type InfinityBitmapWord struct {
	Index int16
	Value *big.Int
}

type InfinityTickSnapshot struct {
	Index          int32
	LiquidityGross *big.Int
	LiquidityNet   *big.Int
}

// Infinity PoolKey fees use the exact dynamic marker below.  A dynamic pool's
// slot0.lpFee is only the manager's last stored/default value; the hook may
// override it for each amount and direction in beforeSwap.  It is therefore
// never a quoteable effective fee by itself.
const (
	infinityDynamicFeeFlag uint32 = 0x800000
	infinityMaxLPFee       uint32 = 1_000_000
	infinityFeeDen         uint32 = 1_000_000
)

// InfinityFeeResolution records why an effective fee is (or is not) usable.
// The status is intentionally kept separate from the numeric pair so callers
// cannot mistake zero values for a resolved zero-fee quote.
type InfinityFeeResolution struct {
	Num      uint32
	Den      uint32
	Resolved bool
	Status   string
}

func unresolvedInfinityFee(status string) InfinityFeeResolution {
	return InfinityFeeResolution{Status: status}
}

// resolveInfinityFee is the only path that may populate a quoteable fee.  It
// requires PoolKey metadata and an internally consistent slot value.  A future
// hook-aware probe can construct a resolved value explicitly; this snapshot
// reader deliberately does not infer one from storage.
func resolveInfinityFee(key *arb.InfinityPoolKey, slot *arb.InfinitySlot0) InfinityFeeResolution {
	if key == nil {
		return unresolvedInfinityFee("pool_key_unavailable")
	}
	if key.Fee == infinityDynamicFeeFlag {
		return unresolvedInfinityFee("dynamic_hook_required")
	}
	if key.Fee > infinityMaxLPFee {
		return unresolvedInfinityFee("pool_key_fee_out_of_range")
	}
	if slot == nil || slot.LPFee != key.Fee {
		return unresolvedInfinityFee("stored_fee_mismatch")
	}
	return InfinityFeeResolution{
		Num:      key.Fee,
		Den:      infinityFeeDen,
		Resolved: true,
		Status:   "static_pool_key",
	}
}

type InfinitySnapshot struct {
	Manager              common.Address
	PoolKey              common.Hash
	Hook                 common.Address
	SqrtPriceX96         *big.Int
	Tick                 int32
	Liquidity            *big.Int
	BitmapWords          []InfinityBitmapWord
	InitializedTicks     []InfinityTickSnapshot
	CoverageMinTick      int32
	CoverageMaxTick      int32
	EffectiveFeeNum      uint32
	EffectiveFeeDen      uint32
	EffectiveFeeResolved bool
	EffectiveFeeStatus   string
}

func floorDiv(a, b int64) int64 {
	q := a / b
	r := a % b
	if r != 0 && ((r > 0) != (b > 0)) {
		q--
	}
	return q
}

const infinityPoolsMappingSlot uint64 = 6

func infinityMappingSlot(id common.Hash, slot uint64) common.Hash {
	var enc [64]byte
	copy(enc[:32], id.Bytes())
	binary.BigEndian.PutUint64(enc[56:], slot)
	return common.BytesToHash(crypto.Keccak256(enc[:]))
}

func infinityNestedMappingSlot(key int64, slot common.Hash) common.Hash {
	var enc [64]byte
	if key < 0 {
		for i := 0; i < 32; i++ {
			enc[i] = 0xff
		}
	}
	binary.BigEndian.PutUint64(enc[24:32], uint64(key))
	copy(enc[32:], slot.Bytes())
	return common.BytesToHash(crypto.Keccak256(enc[:]))
}

func (c *poolCaller) infinityStorageWord(manager common.Address, slot common.Hash) ([]byte, error) {
	return c.staticCall(manager, arb.CallExtsload(slot))
}

// readInfinityStorage supports the deployed BSC Infinity manager revision,
// which exposes extsload but not the later typed getter functions.
func (c *poolCaller) readInfinityStorage(manager common.Address, poolID common.Hash, spacingHint int64) (*InfinitySnapshot, error) {
	base := infinityMappingSlot(poolID, infinityPoolsMappingSlot)
	packed, err := c.infinityStorageWord(manager, base)
	if err != nil {
		return nil, err
	}
	slot, err := arb.DecodeInfinitySlot0Storage(packed)
	if err != nil {
		return nil, err
	}
	liquiditySlot := common.BytesToHash(new(big.Int).Add(new(big.Int).SetBytes(base.Bytes()), big.NewInt(3)).Bytes())
	liqRaw, err := c.infinityStorageWord(manager, liquiditySlot)
	if err != nil {
		return nil, err
	}
	liq, err := arb.DecodeUint128(liqRaw)
	if err != nil {
		return nil, err
	}
	var hook common.Address
	spacing := spacingHint
	if spacing <= 0 {
		return nil, errors.New("arb: infinity tick spacing required for legacy manager")
	}
	if spacing <= 0 || spacing > 16383 {
		return nil, errors.New("arb: infinity invalid tick spacing")
	}
	baseCompressed := floorDiv(int64(slot.Tick), spacing)
	baseWord := floorDiv(baseCompressed, 256)
	if baseWord < -32768 || baseWord > 32767 {
		return nil, errors.New("arb: infinity bitmap word out of range")
	}
	bitmapBase := new(big.Int).Add(new(big.Int).SetBytes(base.Bytes()), big.NewInt(5))
	tickBase := new(big.Int).Add(new(big.Int).SetBytes(base.Bytes()), big.NewInt(4))
	readMap := func(mapBase *big.Int, key int64) ([]byte, error) {
		return c.infinityStorageWord(manager, infinityNestedMappingSlot(key, common.BytesToHash(mapBase.Bytes())))
	}
	words := make([]InfinityBitmapWord, 0, 3)
	ticks := make([]InfinityTickSnapshot, 0)
	minTick, maxTick := int64(1<<31-1), int64(-1<<31)
	for wi := int64(baseWord - 1); wi <= baseWord+1; wi++ {
		if wi < -32768 || wi > 32767 {
			continue
		}
		raw, rerr := readMap(bitmapBase, wi)
		if rerr != nil {
			return nil, rerr
		}
		bitmap, derr := arb.DecodeInfinityBitmap(raw)
		if derr != nil {
			return nil, derr
		}
		words = append(words, InfinityBitmapWord{Index: int16(wi), Value: bitmap})
		lo, hi := wi*256*spacing, (wi*256+255)*spacing
		if lo < minTick {
			minTick = lo
		}
		if hi > maxTick {
			maxTick = hi
		}
		for bit := int64(0); bit < 256; bit++ {
			if bitmap.Bit(int(bit)) == 0 {
				continue
			}
			tick64 := (wi*256 + bit) * spacing
			traw, terr := readMap(tickBase, tick64)
			if terr != nil {
				return nil, terr
			}
			ti, derr := arb.DecodeInfinityTickStorage(traw)
			if derr != nil {
				return nil, derr
			}
			ticks = append(ticks, InfinityTickSnapshot{Index: int32(tick64), LiquidityGross: ti.LiquidityGross, LiquidityNet: ti.LiquidityNet})
		}
	}
	// The legacy extsload layout does not expose a verified PoolKey fee.  Do not
	// use slot.lpFee here: for a dynamic pool it is only a hook-controlled
	// default, and for an unknown layout it cannot establish static identity.
	fee := unresolvedInfinityFee("pool_key_unavailable")
	return &InfinitySnapshot{Manager: manager, PoolKey: poolID, Hook: hook, SqrtPriceX96: slot.SqrtPriceX96, Tick: slot.Tick,
		Liquidity: liq, BitmapWords: words, InitializedTicks: ticks, CoverageMinTick: int32(minTick), CoverageMaxTick: int32(maxTick),
		EffectiveFeeNum: fee.Num, EffectiveFeeDen: fee.Den, EffectiveFeeResolved: fee.Resolved, EffectiveFeeStatus: fee.Status}, nil
}

// ReadInfinityCL reads a Pancake Infinity CL singleton through the manager's
// public getters. Three adjacent bitmap words give the quote engine a bounded,
// honest coverage window; every initialized bit in those words is fetched.
func (c *poolCaller) ReadInfinityCL(manager common.Address, poolID common.Hash) (*InfinitySnapshot, error) {
	return c.readInfinityCL(manager, poolID, 0)
}

func (c *poolCaller) ReadInfinityCLWithSpacing(manager common.Address, poolID common.Hash, spacing int64) (*InfinitySnapshot, error) {
	return c.readInfinityCL(manager, poolID, spacing)
}

func (c *poolCaller) readInfinityCL(manager common.Address, poolID common.Hash, spacingHint int64) (*InfinitySnapshot, error) {
	slotRaw, err := c.staticCall(manager, arb.CallInfinityGetSlot0(poolID))
	if err != nil {
		return c.readInfinityStorage(manager, poolID, spacingHint)
	}
	slot, err := arb.DecodeInfinitySlot0(slotRaw)
	if err != nil {
		return nil, err
	}
	liqRaw, err := c.staticCall(manager, arb.CallInfinityGetLiquidity(poolID))
	if err != nil {
		return nil, err
	}
	liq, err := arb.DecodeUint128(liqRaw)
	if err != nil {
		return nil, err
	}
	keyRaw, err := c.staticCall(manager, arb.CallInfinityPoolKey(poolID))
	if err != nil {
		return nil, err
	}
	key, err := arb.DecodeInfinityPoolKey(keyRaw)
	if err != nil {
		return nil, err
	}
	if common.BytesToAddress(key.PoolManager[:]) != manager {
		return nil, errors.New("arb: infinity pool manager mismatch")
	}
	params := new(big.Int).SetBytes(key.Parameters[:])
	spacing := int64(new(big.Int).Rsh(params, 16).Uint64() & 0xffffff)
	if spacingHint > 0 {
		spacing = spacingHint
	}
	if spacing <= 0 || spacing > 16383 {
		return nil, errors.New("arb: infinity invalid tick spacing")
	}
	baseCompressed := floorDiv(int64(slot.Tick), spacing)
	baseWord := floorDiv(baseCompressed, 256)
	if baseWord < -32768 || baseWord > 32767 {
		return nil, errors.New("arb: infinity bitmap word out of range")
	}
	words := make([]InfinityBitmapWord, 0, 3)
	ticks := make([]InfinityTickSnapshot, 0)
	minTick := int64(1<<31 - 1)
	maxTick := int64(-1 << 31)
	for wi := int64(baseWord - 1); wi <= baseWord+1; wi++ {
		if wi < -32768 || wi > 32767 {
			continue
		}
		word := int16(wi)
		raw, rerr := c.staticCall(manager, arb.CallInfinityGetBitmap(poolID, word))
		if rerr != nil {
			return nil, rerr
		}
		bitmap, derr := arb.DecodeInfinityBitmap(raw)
		if derr != nil {
			return nil, derr
		}
		words = append(words, InfinityBitmapWord{Index: word, Value: bitmap})
		lo := (wi * 256) * spacing
		hi := (wi*256 + 255) * spacing
		if lo < minTick {
			minTick = lo
		}
		if hi > maxTick {
			maxTick = hi
		}
		for bit := int64(0); bit < 256; bit++ {
			if bitmap.Bit(int(bit)) == 0 {
				continue
			}
			tick64 := (wi*256 + bit) * spacing
			if tick64 < -8388608 || tick64 > 8388607 {
				return nil, errors.New("arb: infinity tick out of range")
			}
			traw, terr := c.staticCall(manager, arb.CallInfinityGetTick(poolID, int32(tick64)))
			if terr != nil {
				return nil, terr
			}
			ti, derr := arb.DecodeInfinityTickInfo(traw)
			if derr != nil {
				return nil, derr
			}
			ticks = append(ticks, InfinityTickSnapshot{Index: int32(tick64), LiquidityGross: ti.LiquidityGross, LiquidityNet: ti.LiquidityNet})
		}
	}
	if minTick > maxTick {
		minTick, maxTick = int64(slot.Tick), int64(slot.Tick)
	}
	fee := resolveInfinityFee(key, slot)
	return &InfinitySnapshot{Manager: manager, PoolKey: poolID, Hook: common.BytesToAddress(key.Hooks[:]),
		SqrtPriceX96: slot.SqrtPriceX96, Tick: slot.Tick, Liquidity: liq, BitmapWords: words, InitializedTicks: ticks,
		CoverageMinTick: int32(minTick), CoverageMaxTick: int32(maxTick), EffectiveFeeNum: fee.Num, EffectiveFeeDen: fee.Den,
		EffectiveFeeResolved: fee.Resolved, EffectiveFeeStatus: fee.Status}, nil
}

// ReadV3Head reads slot0() and liquidity() on the pool.
func (c *poolCaller) ReadV3Head(pool common.Address) (*V3Snapshot, error) {
	ret, err := c.staticCall(pool, arb.CallSlot0())
	if err != nil {
		return nil, err
	}
	s0, err := arb.DecodeSlot0(ret)
	if err != nil {
		return nil, err
	}
	lret, err := c.staticCall(pool, arb.CallLiquidity())
	if err != nil {
		return nil, err
	}
	liq, err := arb.DecodeUint128(lret)
	if err != nil {
		return nil, err
	}
	return &V3Snapshot{SqrtPriceX96: s0.SqrtPriceX96, Tick: s0.Tick, Liquidity: liq}, nil
}

// BalanceOf reads ERC20 balanceOf(holder) on the token contract via the same
// budgeted+StaticCall read path as the pool getters. It is the §391 primitive used to
// measure the candidate beneficiary's standardized base-token (e.g. WBNB) retention:
// the base token is an ERC20, so its balance lives in the token's storage mapping —
// NOT a native account balance (GetBalance would read native BNB, which is wrong).
// Any revert / budget trip aborts with an error — never a fabricated balance (§381),
// so the caller can distinguish "unmeasured" from "measured zero".
func (c *poolCaller) BalanceOf(token, holder common.Address) (*big.Int, error) {
	var raw [20]byte
	copy(raw[:], holder.Bytes())
	out, err := c.staticCall(token, arb.CallBalanceOf(raw))
	if err != nil {
		return nil, err
	}
	return arb.DecodeUint256(out)
}

// NewPoolCallerAtHead builds a pool caller at the current canonical head state, for
// integration use. It probes readability through the arb backend contract (a
// readable root is required, design §5.1). ttl/budget bound the read session.
func (s *Ethereum) NewPoolCallerAtHead(maxReads uint64, wall time.Duration) (*poolCaller, error) {
	h := s.blockchain.CurrentBlock()
	if h == nil {
		return nil, errors.New("arb: no canonical head")
	}
	base, err := s.blockchain.StateAt(h.Root)
	if err != nil {
		return nil, err
	}
	budget := arb.NewReadBudget(func() time.Time { return time.Now() }, maxReads, wall)
	return s.newPoolCaller(base, h, budget), nil
}
