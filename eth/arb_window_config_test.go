package eth

import (
	"bytes"
	"errors"
	"math/big"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/eth/arb"
)

func withArbWindow(t *testing.T, c arbWindowConfig) {
	t.Helper()
	prev := arbWindowSetting
	arbWindowSetting = c
	t.Cleanup(func() { arbWindowSetting = prev })
}

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestArbWindowConfigDefaultsAreLegacy(t *testing.T) {
	c := loadArbWindowConfig(envMap(nil))
	if !c.IsLegacy() || c.V3Radius != 2 || c.V3TicksPerSide != 16 || c.SingletonRadius != 1 || len(c.Invalid) != 0 {
		t.Fatalf("defaults drifted from legacy: %s", c)
	}
	c = loadArbWindowConfig(envMap(map[string]string{envV3WindowRadius: "4", envV3TicksPerSide: "48", envSingletonWindowRadius: "3", envSingletonExtTicksPerSide: "0"}))
	if c.V3Radius != 4 || c.V3TicksPerSide != 48 || c.SingletonRadius != 3 || c.SingletonExtTicksSide != 0 || c.IsLegacy() {
		t.Fatalf("explicit values not applied: %s", c)
	}
	c = loadArbWindowConfig(envMap(map[string]string{envV3WindowRadius: "99", envV3TicksPerSide: "4", envSingletonWindowRadius: "x", envSingletonExtTicksPerSide: "-1"}))
	if !c.IsLegacy() || c.SingletonExtTicksSide != defaultSingletonExtTicksPerSide || len(c.Invalid) != 4 {
		t.Fatalf("out-of-range values must fall back to defaults: %s %v", c, c.Invalid)
	}
}

// fakeV3 serves tickSpacing=1 and a bitmap per word; every initialized tick
// has liquidityGross=100, liquidityNet=-1. reads counts ticks(int24) calls.
func fakeV3(bitmaps map[int64]*big.Int, reads *int, words *[]int64) func([]byte) ([]byte, error) {
	return func(data []byte) ([]byte, error) {
		switch {
		case bytes.Equal(data[:4], v3ViewCall("tickSpacing()", nil)):
			return w32(big.NewInt(1)), nil
		case bytes.Equal(data[:4], v3ViewCall("tickBitmap(int16)", nil)):
			word := new(big.Int).SetBytes(data[4:36])
			if data[4] == 0xff {
				word.Sub(word, new(big.Int).Lsh(big.NewInt(1), 256))
			}
			*words = append(*words, word.Int64())
			out := make([]byte, 32)
			if b, ok := bitmaps[word.Int64()]; ok {
				b.FillBytes(out)
			}
			return out, nil
		case bytes.Equal(data[:4], v3ViewCall("ticks(int24)", nil)):
			*reads++
			out := make([]byte, 256)
			copy(out[:32], w32(big.NewInt(100)))
			copy(out[32:64], bytes.Repeat([]byte{0xff}, 32))
			out[255] = 1
			return out, nil
		}
		return nil, errors.New("unexpected selector")
	}
}

func bits(idx ...int) *big.Int {
	b := new(big.Int)
	for _, i := range idx {
		b.SetBit(b, i, 1)
	}
	return b
}

func TestV3WindowWidenedPublishesCompleteExtendedWords(t *testing.T) {
	bm := map[int64]*big.Int{-4: bits(7), 0: bits(3, 9), 3: bits(1, 2), 4: bits(5)}
	// Legacy: 5 words, coverage [-512, 767], extended ticks never touched.
	reads, seen := 0, []int64{}
	snap, err := readV3Window(arb.PoolSnapshot{Tick: "5"}, 1, fakeV3(bm, &reads, &seen))
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 5 || len(snap.BitmapWords) != 5 || reads != 2 || snap.CoverageMinTick != "-512" || snap.CoverageMaxTick != "767" {
		t.Fatalf("legacy window changed: words=%v reads=%d snap=%+v", seen, reads, snap)
	}
	withArbWindow(t, arbWindowConfig{V3Radius: 4, V3TicksPerSide: 16, SingletonRadius: 1})
	reads, seen = 0, nil
	snap, err = readV3Window(arb.PoolSnapshot{Tick: "5"}, 1, fakeV3(bm, &reads, &seen))
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 9 || len(snap.BitmapWords) != 9 || reads != 6 || snap.CoverageMinTick != "-1024" || snap.CoverageMaxTick != "1279" {
		t.Fatalf("widened window wrong: words=%v reads=%d snap=%+v", seen, reads, snap)
	}
	if len(snap.InitializedTicks) != 6 {
		t.Fatalf("ticks lost: %+v", snap.InitializedTicks)
	}
}

func TestV3WindowTrimsExtendedWordBeyondTickCap(t *testing.T) {
	full := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	// Word +3 is dense (256 ticks): with 16 ticks per side it cannot be fully
	// read, so the upper side stops at +2 while the sparse lower side widens.
	bm := map[int64]*big.Int{0: bits(1), 3: full, -3: bits(4)}
	withArbWindow(t, arbWindowConfig{V3Radius: 4, V3TicksPerSide: 16, SingletonRadius: 1})
	reads, seen := 0, []int64{}
	snap, err := readV3Window(arb.PoolSnapshot{Tick: "100"}, 1, fakeV3(bm, &reads, &seen))
	if err != nil {
		t.Fatal(err)
	}
	if snap.CoverageMinTick != "-1024" || snap.CoverageMaxTick != "767" || len(snap.BitmapWords) != 7 {
		t.Fatalf("dense extended word must not be published: %+v", snap)
	}
	for _, tk := range snap.InitializedTicks {
		v, _ := strconv.ParseInt(tk.Index, 10, 64)
		if v > 767 || v < -1024 {
			t.Fatalf("tick outside published coverage: %v", v)
		}
	}
}

func TestSingletonWindowLegacyAndWidened(t *testing.T) {
	bm := map[int64]*big.Int{-3: bits(1, 2, 3), -1: bits(0), 0: bits(10), 2: bits(255)}
	var bitmapReads, tickReads int
	rd := singletonWindowReader{
		bitmap: func(wi int64) (*big.Int, error) {
			bitmapReads++
			if b, ok := bm[wi]; ok {
				return b, nil
			}
			return new(big.Int), nil
		},
		tick: func(t int64) (InfinityTickSnapshot, error) {
			tickReads++
			return InfinityTickSnapshot{Index: int32(t), LiquidityGross: big.NewInt(1), LiquidityNet: big.NewInt(1)}, nil
		},
	}
	words, ticks, lo, hi, err := readSingletonWindow(0, 10, defaultArbWindowConfig(), rd)
	if err != nil || len(words) != 3 || len(ticks) != 2 || lo != -2560 || hi != 5110 || bitmapReads != 3 {
		t.Fatalf("legacy singleton window changed: words=%d ticks=%d lo=%d hi=%d reads=%d err=%v", len(words), len(ticks), lo, hi, bitmapReads, err)
	}
	bitmapReads, tickReads = 0, 0
	cfg := arbWindowConfig{V3Radius: 2, V3TicksPerSide: 16, SingletonRadius: 3, SingletonExtTicksSide: 2}
	words, ticks, lo, hi, err = readSingletonWindow(0, 10, cfg, rd)
	if err != nil {
		t.Fatal(err)
	}
	// Upper side: +2 (1 tick) and +3 (0) published; lower: -2 (0) published,
	// -3 (3 ticks > cap 2) stops the side.
	if len(words) != 6 || words[0].Index != -2 || words[5].Index != 3 || lo != -5120 || hi != (3*256+255)*10 || len(ticks) != 3 {
		t.Fatalf("widened singleton window wrong: words=%+v ticks=%d lo=%d hi=%d", words, len(ticks), lo, hi)
	}
	for i := 1; i < len(ticks); i++ {
		if ticks[i-1].Index >= ticks[i].Index {
			t.Fatal("ticks not sorted")
		}
	}
}
