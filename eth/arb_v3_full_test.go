package eth

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/eth/arb"
)

func TestV3PostWindowNegativeTickAndBoundedReads(t *testing.T) {
	reads := 0
	call := func(data []byte) ([]byte, error) {
		switch {
		case bytes.Equal(data[:4], v3ViewCall("tickSpacing()", nil)):
			return w32(big.NewInt(1)), nil
		case bytes.Equal(data[:4], v3ViewCall("tickBitmap(int16)", nil)):
			return bytes.Repeat([]byte{0xff}, 32), nil
		case bytes.Equal(data[:4], v3ViewCall("ticks(int24)", nil)):
			reads++
			out := make([]byte, 256)
			copy(out[:32], w32(big.NewInt(100)))
			copy(out[32:64], bytes.Repeat([]byte{0xff}, 32)) // int128 -1
			out[255] = 1
			return out, nil
		}
		return nil, errors.New("unexpected selector")
	}
	snap, err := readV3Window(arb.PoolSnapshot{Kind: "v3", Tick: "-1"}, 1, call)
	if err != nil {
		t.Fatal(err)
	}
	if reads != 32 || len(snap.BitmapWords) != 5 || snap.BitmapWords[0].Index != "-3" || snap.CoverageMinTick != "-768" || snap.CoverageMaxTick != "511" {
		t.Fatalf("incorrect bounded negative window: reads=%d snapshot=%+v", reads, snap)
	}
	if snap.InitializedTicks[0].Index != "-1" || snap.InitializedTicks[0].LiquidityNet != "-1" {
		t.Fatal("lost signed tick or liquidity")
	}
	wire := poolSnapshotWire(snap)
	if len(wire["bitmap_words"].([]any)) != 5 || len(wire["initialized_ticks"].([]any)) != 32 {
		t.Fatal("wire lost post-state pages")
	}
	// A missing initialized tick is retained in the bitmap, not cleared to make
	// quotes appear complete beyond the fetched nearest ticks.
	if snap.BitmapWords[0].Value != new(big.Int).SetBytes(bytes.Repeat([]byte{0xff}, 32)).String() {
		t.Fatal("fabricated bitmap")
	}
}

func TestV3PostWindowRejectsMissingPagesAndWrongSpacing(t *testing.T) {
	boom := errors.New("read budget exhausted")
	for _, expected := range []int32{1, 2} {
		_, err := readV3Window(arb.PoolSnapshot{Tick: "0"}, expected, func(data []byte) ([]byte, error) {
			if bytes.Equal(data[:4], v3ViewCall("tickSpacing()", nil)) {
				return w32(big.NewInt(1)), nil
			}
			return nil, boom
		})
		if err == nil {
			t.Fatal("accepted incomplete or mismatched state")
		}
		if expected == 1 && !errors.Is(err, boom) {
			t.Fatal("lost budget error")
		}
	}
}

func TestDecodeV3TickRecordAcceptsOnlyVerifiedLayouts(t *testing.T) {
	standard := make([]byte, 8*32)
	copy(standard[:32], w32(big.NewInt(100)))
	standard[len(standard)-1] = 1
	if _, err := decodeV3TickRecord(standard); err != nil {
		t.Fatalf("standard V3 tick rejected: %v", err)
	}

	extended := make([]byte, 10*32)
	copy(extended[:32], w32(big.NewInt(100)))
	copy(extended[2*32:9*32], bytes.Repeat([]byte{0xab}, 7*32))
	extended[len(extended)-1] = 1
	if _, err := decodeV3TickRecord(extended); err != nil {
		t.Fatalf("extended V3 tick rejected: %v", err)
	}

	invalidFlag := append([]byte(nil), extended...)
	invalidFlag[len(invalidFlag)-1] = 2
	if _, err := decodeV3TickRecord(invalidFlag); err == nil {
		t.Fatal("accepted non-canonical initialized flag")
	}
	if _, err := decodeV3TickRecord(make([]byte, 9*32)); err == nil {
		t.Fatal("accepted unverified V3 tick width")
	}
}
