// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package legacypool

import (
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"testing"
)

func capturedPoolPrices(tb testing.TB) []uint64 {
	tb.Helper()
	data, err := os.ReadFile("testdata/priced_pool_20261024.json")
	if err != nil {
		tb.Fatal(err)
	}
	var fixture struct {
		Pending int         `json:"pending"`
		Queued  int         `json:"queued"`
		Prices  [][2]uint64 `json:"price_wei_counts"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		tb.Fatal(err)
	}
	var prices []uint64
	for i, entry := range fixture.Prices {
		if entry[1] == 0 || (i > 0 && entry[0] <= fixture.Prices[i-1][0]) {
			tb.Fatalf("invalid price histogram entry %d", i)
		}
		for n := uint64(0); n < entry[1]; n++ {
			prices = append(prices, entry[0])
		}
	}
	if fixture.Pending != 38_292 || fixture.Queued != 582 || len(prices) != fixture.Pending+fixture.Queued {
		tb.Fatalf("captured pool counts differ: pending=%d queued=%d prices=%d", fixture.Pending, fixture.Queued, len(prices))
	}
	return prices
}

func TestCapturedPoolPriceFixture(t *testing.T) {
	capturedPoolPrices(t)
}

// BenchmarkPricedReheapCapturedPool measures the entire production Reheap,
// including lookup.Range, allocation, both heap.Init calls and the 20% split.
// Prices/counts are observed aggregates; types, nonces and recipients are synthetic.
func BenchmarkPricedReheapCapturedPool(b *testing.B) {
	prices := capturedPoolPrices(b)
	for _, size := range []int{len(prices), 80_000} {
		for _, mixed := range []bool{false, true} {
			name := "Legacy"
			if mixed {
				name = "LegacyDynamic50_50"
			}
			b.Run(fmt.Sprintf("%s/%d", name, size), func(b *testing.B) {
				workload := make([]uint64, size)
				for i := range workload {
					workload[i] = prices[i%len(prices)]
				}
				rng := rand.New(rand.NewSource(24))
				rng.Shuffle(len(workload), func(i, j int) { workload[i], workload[j] = workload[j], workload[i] })
				all := newLookup()
				for i, price := range workload {
					id := 4 * i // Legacy by default; alternate DynamicFee for the mixed case.
					if mixed {
						id += 2 * (i % 2)
					}
					all.Add(comparisonTransaction(id, price, price, nil))
				}
				if all.Count() != size {
					b.Fatalf("lookup count: have %d, want %d", all.Count(), size)
				}
				priced := newPricedList(all)
				priced.SetBaseFee(new(big.Int))
				b.ReportAllocs()
				for b.Loop() {
					priced.Reheap()
				}
				b.ReportMetric(float64(size), "txs")
			})
		}
	}
}
