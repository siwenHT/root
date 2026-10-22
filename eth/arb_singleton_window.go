package eth

import (
	"math/big"
	"sort"
)

// singletonWindowReader abstracts the two Infinity/V4 read paths (typed
// getters and extsload storage) so both share one window walk.
type singletonWindowReader struct {
	bitmap func(word int64) (*big.Int, error)
	tick   func(tick int64) (InfinityTickSnapshot, error)
}

// readSingletonWindow reads the legacy base?1 words in ascending order with
// every initialized tick (unchanged behaviour), then, when the configured
// radius is wider, walks outward one side at a time. An extended word is
// published only after all of its initialized ticks are read; a word that
// would exceed the per-side extended tick cap ends that side unpublished, so
// coverage stays contiguous and honest. Returned min/max are the coverage
// bounds of the published words (min > max when nothing was published).
func readSingletonWindow(baseWord, spacing int64, cfg arbWindowConfig, rd singletonWindowReader) ([]InfinityBitmapWord, []InfinityTickSnapshot, int64, int64, error) {
	words := make([]InfinityBitmapWord, 0, 2*cfg.SingletonRadius+1)
	ticks := make([]InfinityTickSnapshot, 0)
	minTick, maxTick := int64(1<<31-1), int64(-1<<31)
	emit := func(wi int64, bitmap *big.Int) error {
		for bit := int64(0); bit < 256; bit++ {
			if bitmap.Bit(int(bit)) == 0 {
				continue
			}
			t, err := rd.tick((wi*256 + bit) * spacing)
			if err != nil {
				return err
			}
			ticks = append(ticks, t)
		}
		words = append(words, InfinityBitmapWord{Index: int16(wi), Value: bitmap})
		lo, hi := wi*256*spacing, (wi*256+255)*spacing
		if lo < minTick {
			minTick = lo
		}
		if hi > maxTick {
			maxTick = hi
		}
		return nil
	}
	inner := cfg.SingletonRadius
	if inner > legacySingletonWindowRadius {
		inner = legacySingletonWindowRadius
	}
	for wi := baseWord - inner; wi <= baseWord+inner; wi++ {
		if wi < -32768 || wi > 32767 {
			continue
		}
		bitmap, err := rd.bitmap(wi)
		if err != nil {
			return nil, nil, 0, 0, err
		}
		if err := emit(wi, bitmap); err != nil {
			return nil, nil, 0, 0, err
		}
	}
	if cfg.SingletonRadius <= inner {
		return words, ticks, minTick, maxTick, nil
	}
	for _, dir := range []int64{-1, 1} {
		used := 0
		for d := inner + 1; d <= cfg.SingletonRadius; d++ {
			wi := baseWord + dir*d
			if wi < -32768 || wi > 32767 {
				break
			}
			bitmap, err := rd.bitmap(wi)
			if err != nil {
				return nil, nil, 0, 0, err
			}
			n := 0
			for _, w := range bitmap.Bits() {
				n += popcountWord(uint64(w))
			}
			if cfg.SingletonExtTicksSide > 0 && used+n > cfg.SingletonExtTicksSide {
				break
			}
			used += n
			if err := emit(wi, bitmap); err != nil {
				return nil, nil, 0, 0, err
			}
		}
	}
	sort.Slice(words, func(i, j int) bool { return words[i].Index < words[j].Index })
	sort.Slice(ticks, func(i, j int) bool { return ticks[i].Index < ticks[j].Index })
	return words, ticks, minTick, maxTick, nil
}

func popcountWord(x uint64) int {
	n := 0
	for x != 0 {
		x &= x - 1
		n++
	}
	return n
}
