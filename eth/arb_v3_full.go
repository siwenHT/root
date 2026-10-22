package eth

import (
	"errors"
	"math/big"
	"sort"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

// ReadV3Full reads a bounded tick window through the same StaticCall instance
// as slot0/liquidity. On a post handle, ALL fields therefore include the target's
// writes. Missing tick records remain missing, never synthesized as zero.
func (c *poolCaller) ReadV3Full(pool common.Address, expectedSpacing int32) (arb.PoolSnapshot, error) {
	head, err := c.ReadV3Head(pool)
	if err != nil {
		return arb.PoolSnapshot{}, err
	}
	snap := arb.PoolSnapshot{Locator: pool.Hex(), Kind: "v3", SqrtPriceX96: head.SqrtPriceX96.String(),
		Tick: strconv.FormatInt(int64(head.Tick), 10), Liquidity: head.Liquidity.String()}
	return readV3Full(snap, expectedSpacing, func(data []byte) ([]byte, error) { return c.staticCall(pool, data) })
}

// readV3Full binds the pool's mutable fee() value to the same parent-state
// view as slot0, liquidity, bitmap and ticks. A marker pool without this value
// is intentionally not quotable by the Rust side.
func readV3Full(snap arb.PoolSnapshot, expectedSpacing int32, call func([]byte) ([]byte, error)) (arb.PoolSnapshot, error) {
	snap, err := readV3Window(snap, expectedSpacing, call)
	if err != nil {
		return arb.PoolSnapshot{}, err
	}
	raw, err := call(v3ViewCall("fee()", nil))
	if err != nil {
		return arb.PoolSnapshot{}, err
	}
	fee, err := arb.DecodeUint256(raw)
	if err != nil || !fee.IsUint64() || fee.Uint64() >= 1_000_000 {
		return arb.PoolSnapshot{}, errors.New("arb: v3 fee invalid")
	}
	snap.EffectiveFeeNum = strconv.FormatUint(fee.Uint64(), 10)
	snap.EffectiveFeeDen = "1000000"
	snap.EffectiveFeeResolved = true
	snap.EffectiveFeeStatus = "resolved"
	return snap, nil
}

func v3ViewCall(signature string, arg *int64) []byte {
	data := append([]byte{}, crypto.Keccak256([]byte(signature))[:4]...)
	if arg != nil {
		word := make([]byte, 32)
		if *arg < 0 {
			for i := range word {
				word[i] = 0xff
			}
		}
		for i := 0; i < 8; i++ {
			word[31-i] = byte(uint64(*arg) >> (8 * i))
		}
		data = append(data, word...)
	}
	return data
}

func readV3Window(snap arb.PoolSnapshot, expectedSpacing int32, call func([]byte) ([]byte, error)) (arb.PoolSnapshot, error) {
	raw, err := call(v3ViewCall("tickSpacing()", nil))
	if err != nil {
		return arb.PoolSnapshot{}, err
	}
	spacing, err := arb.DecodeUint256(raw)
	if err != nil || !spacing.IsInt64() || spacing.Int64() < 1 || spacing.Int64() > 16383 || spacing.Int64() != int64(expectedSpacing) {
		return arb.PoolSnapshot{}, errors.New("arb: v3 tick spacing invalid")
	}
	tick, err := strconv.ParseInt(snap.Tick, 10, 32)
	if err != nil || tick < -887272 || tick > 887272 {
		return arb.PoolSnapshot{}, errors.New("arb: v3 tick invalid")
	}
	step := spacing.Int64()
	base := floorDiv(floorDiv(tick, step), 256)
	cfg := arbWindowSetting
	radius := cfg.V3Radius
	first, last := base-radius, base+radius
	if first < -32768 || last > 32767 {
		return arb.PoolSnapshot{}, errors.New("arb: v3 word range invalid")
	}
	bitmaps := make([]*big.Int, 0, last-first+1)
	wordTicks := make(map[int64][]int64, last-first+1)
	indices := make([]int64, 0)
	for word := first; word <= last; word++ {
		raw, err := call(v3ViewCall("tickBitmap(int16)", &word))
		if err != nil {
			return arb.PoolSnapshot{}, err
		}
		if len(raw) != 32 {
			return arb.PoolSnapshot{}, errors.New("arb: v3 bitmap width invalid")
		}
		bitmap := new(big.Int).SetBytes(raw)
		bitmaps = append(bitmaps, bitmap)
		for bit := int64(0); bit < 256; bit++ {
			index := (word*256 + bit) * step
			if bitmap.Bit(int(bit)) != 0 && index >= -887272 && index <= 887272 {
				indices = append(indices, index)
				wordTicks[word] = append(wordTicks[word], index)
			}
		}
	}
	// Bound EVM reads to the nearest N initialized ticks on either side (legacy
	// N=16), while retaining the real bitmap so clients stop with CacheMiss
	// beyond coverage.
	sort.Slice(indices, func(i, j int) bool {
		a, b := indices[i]-tick, indices[j]-tick
		if a < 0 {
			a = -a
		}
		if b < 0 {
			b = -b
		}
		if a == b {
			return indices[i] < indices[j]
		}
		return a < b
	})
	perSide := cfg.V3TicksPerSide
	lower, upper := 0, 0
	read := make(map[int64]arb.InfinityTick, 2*perSide)
	ordered := make([]int64, 0, 2*perSide)
	for _, index := range indices {
		if index <= tick {
			if lower >= perSide {
				continue
			}
			lower++
		} else {
			if upper >= perSide {
				continue
			}
			upper++
		}
		raw, err := call(v3ViewCall("ticks(int24)", &index))
		if err != nil {
			return arb.PoolSnapshot{}, err
		}
		ti, err := decodeV3TickRecord(raw)
		if err != nil {
			return arb.PoolSnapshot{}, err
		}
		read[index] = arb.InfinityTick{Index: strconv.FormatInt(index, 10), LiquidityGross: ti.LiquidityGross.String(), LiquidityNet: ti.LiquidityNet.String()}
		ordered = append(ordered, index)
	}
	// Legacy words (base?2, or fewer if the radius is narrower) are always
	// published, exactly as before. An extended word is published only if every
	// initialized tick in it, and in every word between it and the legacy
	// window, was read; the first incomplete word ends that side.
	inner := radius
	if inner > legacyV3WindowRadius {
		inner = legacyV3WindowRadius
	}
	complete := func(word int64) bool {
		for _, index := range wordTicks[word] {
			if _, ok := read[index]; !ok {
				return false
			}
		}
		return true
	}
	emitLo, emitHi := base-inner, base+inner
	for word := base - inner - 1; word >= first && complete(word); word-- {
		emitLo = word
	}
	for word := base + inner + 1; word <= last && complete(word); word++ {
		emitHi = word
	}
	for word := emitLo; word <= emitHi; word++ {
		snap.BitmapWords = append(snap.BitmapWords, arb.InfinityBitmapWord{Index: strconv.FormatInt(word, 10), Value: bitmaps[word-first].String()})
	}
	minIndex, maxIndex := emitLo*256*step, (emitHi+1)*256*step-1
	for _, index := range ordered {
		if index >= minIndex && index <= maxIndex {
			snap.InitializedTicks = append(snap.InitializedTicks, read[index])
		}
	}
	snap.CoverageMinTick = strconv.FormatInt(minIndex, 10)
	snap.CoverageMaxTick = strconv.FormatInt(maxIndex, 10)
	return snap, nil
}

// decodeV3TickRecord accepts the standard V3 Tick.Info tuple (8 ABI words) and
// the verified extended implementation used by 0x767f...65b4 (10 ABI words).
// Both layouts put liquidityGross and liquidityNet in the first two words and a
// canonical initialized flag in the final word. Other widths are rejected so a
// different ABI cannot be mistaken for V3 state.
func decodeV3TickRecord(raw []byte) (*arb.InfinityTickInfo, error) {
	if len(raw) != 8*32 && len(raw) != 10*32 {
		return nil, errors.New("arb: v3 tick record invalid")
	}
	if new(big.Int).SetBytes(raw[len(raw)-32:]).Cmp(big.NewInt(1)) != 0 {
		return nil, errors.New("arb: v3 tick record invalid")
	}
	ti, err := arb.DecodeInfinityTickInfo(raw)
	if err != nil || ti.LiquidityGross.Sign() == 0 {
		return nil, errors.New("arb: v3 tick liquidity invalid")
	}
	return ti, nil
}
