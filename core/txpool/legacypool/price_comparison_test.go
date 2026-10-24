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
	"container/heap"
	"fmt"
	"math/big"
	"math/rand"
	"slices"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// priceCmpBeforeZeroBaseFee uses the unchanged conversion-based calculation,
// independent of EffectiveGasTipCmp's fast path. Heap tie breaks are unchanged.
func priceCmpBeforeZeroBaseFee(a, b *types.Transaction, base *uint256.Int) int {
	if base != nil {
		aTip, _ := a.EffectiveGasTip(base.ToBig())
		bTip, _ := b.EffectiveGasTip(base.ToBig())
		if c := aTip.Cmp(bTip); c != 0 {
			return c
		}
	}
	if c := a.GasFeeCapCmp(b); c != 0 {
		return c
	}
	return a.GasTipCapCmp(b)
}

func priceLessBeforeZeroBaseFee(a, b *types.Transaction, base *uint256.Int) bool {
	if c := priceCmpBeforeZeroBaseFee(a, b, base); c != 0 {
		return c < 0
	}
	return a.Nonce() > b.Nonce()
}

type priceHeapBeforeZeroBaseFee struct{ priceHeap }

func (h *priceHeapBeforeZeroBaseFee) Less(i, j int) bool {
	return priceLessBeforeZeroBaseFee(h.list[i], h.list[j], h.baseFee)
}

func comparisonTransaction(i int, tip, fee uint64, data []byte) *types.Transaction {
	to := common.BigToAddress(big.NewInt(int64(i + 1)))
	switch i % 4 {
	case 0:
		return types.NewTx(&types.LegacyTx{Nonce: uint64(i), GasPrice: new(big.Int).SetUint64(fee), Gas: 21000, To: &to, Data: data})
	case 1:
		return types.NewTx(&types.AccessListTx{Nonce: uint64(i), GasPrice: new(big.Int).SetUint64(fee), Gas: 21000, To: &to, Data: data})
	case 2:
		return types.NewTx(&types.DynamicFeeTx{Nonce: uint64(i), GasTipCap: new(big.Int).SetUint64(tip), GasFeeCap: new(big.Int).SetUint64(fee), Gas: 21000, To: &to, Data: data})
	default:
		return types.NewTx(&types.SetCodeTx{Nonce: uint64(i), GasTipCap: uint256.NewInt(tip), GasFeeCap: uint256.NewInt(fee), Gas: 21000, To: to, Data: data})
	}
}

func TestPriceHeapOrderingDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(24))
	txs := make(types.Transactions, 256)
	for i := range txs {
		fee := uint64(rng.Intn(8))
		txs[i] = comparisonTransaction(i, uint64(rng.Intn(12)), fee, nil)
	}
	// Add equal fee/tip/nonce priorities with different payloads. Starting from the
	// same frozen input must preserve even the otherwise unspecified tie order.
	for i := 0; i < 16; i++ {
		txs = append(txs, types.NewTx(&types.DynamicFeeTx{Nonce: 7, GasTipCap: big.NewInt(3), GasFeeCap: big.NewInt(5), Data: []byte{byte(i)}}))
	}
	for _, base := range []*uint256.Int{nil, new(uint256.Int), uint256.NewInt(4), uint256.NewInt(1_000_000_000)} {
		t.Run(fmt.Sprint(base), func(t *testing.T) {
			current := &priceHeap{baseFee: base, list: slices.Clone(txs)}
			original := &priceHeapBeforeZeroBaseFee{priceHeap{baseFee: base, list: slices.Clone(txs)}}
			heap.Init(current)
			heap.Init(original)
			for current.Len() > 0 {
				got := heap.Pop(current).(*types.Transaction)
				want := heap.Pop(original).(*types.Transaction)
				if got.Hash() != want.Hash() {
					t.Fatalf("pop %d: have %s, want %s", current.Len(), got.Hash(), want.Hash())
				}
			}
		})
	}
}

// sortedPriceOracle models selection with sorted slices, not the production
// heaps. Unique nonces make full priorities distinct, allowing exact membership
// and eviction comparisons despite lookup.Range's randomized map order.
type sortedPriceOracle struct {
	all              map[common.Hash]*types.Transaction
	urgent, floating types.Transactions
	base             *uint256.Int
	stales           int64
}

func (o *sortedPriceOracle) sort(txs types.Transactions, base *uint256.Int) {
	sort.Slice(txs, func(i, j int) bool { return priceLessBeforeZeroBaseFee(txs[i], txs[j], base) })
}

func (o *sortedPriceOracle) reheap() {
	o.urgent = nil
	for _, tx := range o.all {
		o.urgent = append(o.urgent, tx)
	}
	o.sort(o.urgent, o.base)
	n := len(o.urgent) / 5
	o.floating = slices.Clone(o.urgent[:n])
	o.urgent = slices.Clone(o.urgent[n:])
	o.sort(o.floating, nil)
	o.stales = 0
}

func (o *sortedPriceOracle) underpricedFor(txs *types.Transactions, tx *types.Transaction, base *uint256.Int) bool {
	for len(*txs) > 0 && o.all[(*txs)[0].Hash()] == nil {
		*txs = (*txs)[1:]
		o.stales--
	}
	return len(*txs) > 0 && priceCmpBeforeZeroBaseFee((*txs)[0], tx, base) >= 0
}

func (o *sortedPriceOracle) underpriced(tx *types.Transaction) bool {
	return (o.underpricedFor(&o.urgent, tx, o.base) || len(o.urgent) == 0) &&
		(o.underpricedFor(&o.floating, tx, nil) || len(o.floating) == 0) &&
		(len(o.urgent) != 0 || len(o.floating) != 0)
}

func (o *sortedPriceOracle) discard(slots int) (types.Transactions, bool) {
	var dropped types.Transactions
	for slots > 0 && len(o.urgent)+len(o.floating) > 0 {
		if len(o.urgent) > 4*len(o.floating) {
			tx := o.urgent[0]
			o.urgent = o.urgent[1:]
			if o.all[tx.Hash()] == nil {
				o.stales--
				continue
			}
			o.floating = append(o.floating, tx)
			o.sort(o.floating, nil)
		} else {
			tx := o.floating[0]
			o.floating = o.floating[1:]
			if o.all[tx.Hash()] == nil {
				o.stales--
				continue
			}
			dropped = append(dropped, tx)
			slots -= numSlots(tx)
		}
	}
	if slots > 0 {
		o.urgent = append(o.urgent, dropped...)
		o.sort(o.urgent, o.base)
		return nil, false
	}
	return dropped, true
}

func checkPriceOracle(t *testing.T, p *pricedList, o *sortedPriceOracle) {
	t.Helper()
	if got := p.stales.Load(); got != o.stales {
		t.Fatalf("stales: have %d, want %d", got, o.stales)
	}
	if got := p.all.Count(); got != len(o.all) {
		t.Fatalf("lookup: have %d, want %d", got, len(o.all))
	}
	for _, pair := range []struct {
		name string
		heap *priceHeap
		want types.Transactions
	}{{"urgent", &p.urgent, o.urgent}, {"floating", &p.floating, o.floating}} {
		for i := 1; i < pair.heap.Len(); i++ {
			if pair.heap.Less(i, (i-1)/2) {
				t.Fatalf("%s heap order broken at %d", pair.name, i)
			}
		}
		got := slices.Clone(pair.heap.list)
		o.sort(got, pair.heap.baseFee)
		if len(got) != len(pair.want) {
			t.Fatalf("%s length: have %d, want %d", pair.name, len(got), len(pair.want))
		}
		for i := range got {
			if got[i].Hash() != pair.want[i].Hash() {
				t.Fatalf("%s item %d: have %s, want %s", pair.name, i, got[i].Hash(), pair.want[i].Hash())
			}
		}
	}
}

func TestPricedListEvictionDifferential(t *testing.T) {
	for _, initial := range []*uint256.Int{nil, new(uint256.Int), uint256.NewInt(4)} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			p := newPricedList(newLookup())
			p.urgent.baseFee = initial
			o := &sortedPriceOracle{all: make(map[common.Hash]*types.Transaction), base: initial}
			rng := rand.New(rand.NewSource(24))
			var txs types.Transactions
			add := func(i int) {
				fee := uint64(rng.Intn(16) + 1)
				var data []byte
				if i%31 == 0 {
					data = make([]byte, txSlotSize+1) // Exercise multi-slot eviction.
				}
				tx := comparisonTransaction(i, uint64(rng.Intn(int(fee)+1)), fee, data)
				txs = append(txs, tx)
				p.all.Add(tx)
				p.Put(tx)
				o.all[tx.Hash()] = tx
				o.urgent = append(o.urgent, tx)
				o.sort(o.urgent, o.base)
			}
			for i := 0; i < 160; i++ {
				add(i)
			}
			p.Reheap()
			o.reheap()
			checkPriceOracle(t, p, o)
			for step := 0; step < 60; step++ {
				switch step % 5 {
				case 0:
					add(160 + step)
				case 1:
					// Force stale-head cleanup rather than relying on random removals.
					for _, h := range []*priceHeap{&p.urgent, &p.floating} {
						if len(h.list) > 0 && o.all[h.list[0].Hash()] != nil {
							tx := h.list[0]
							p.all.Remove(tx.Hash())
							delete(o.all, tx.Hash())
							p.Removed(1)
							o.stales++
							break
						}
					}
				case 2:
					for _, tx := range append(slices.Clone(txs), comparisonTransaction(10_000, 100, 100, nil)) {
						if got, want := p.Underpriced(tx), o.underpriced(tx); got != want {
							t.Fatalf("step %d underpriced %s: have %t, want %t", step, tx.Hash(), got, want)
						}
					}
				case 3:
					slots := 7
					if step == 3 || step == 33 {
						slots = p.all.Slots() + 1 // Exhaustion must roll the selection back.
					}
					got, ok := p.Discard(slots)
					want, wantOK := o.discard(slots)
					if ok != wantOK || len(got) != len(want) {
						t.Fatalf("step %d discard: have %d/%t, want %d/%t", step, len(got), ok, len(want), wantOK)
					}
					for i, tx := range got {
						if tx.Hash() != want[i].Hash() {
							t.Fatalf("step %d drop %d: have %s, want %s", step, i, tx.Hash(), want[i].Hash())
						}
						p.all.Remove(tx.Hash())
						delete(o.all, tx.Hash())
					}
				case 4:
					var base *big.Int
					if step%3 != 0 {
						base = big.NewInt(int64(step % 7))
					}
					p.SetBaseFee(base) // Includes nil, zero, and nonzero transitions.
					o.base = new(uint256.Int)
					if base != nil {
						o.base.SetFromBig(base)
					}
					o.reheap()
				}
				checkPriceOracle(t, p, o)
			}
			// Demotion removes entries without calling Removed; SetBaseFee must still
			// rebuild and purge them when the base fee remains zero.
			p.SetBaseFee(new(big.Int))
			o.base = new(uint256.Int)
			o.reheap()
			for _, tx := range txs {
				if o.all[tx.Hash()] != nil {
					p.all.Remove(tx.Hash())
					delete(o.all, tx.Hash())
					break
				}
			}
			p.SetBaseFee(new(big.Int))
			o.reheap()
			checkPriceOracle(t, p, o)
			// Removed's threshold must also rebuild with exactly the live set.
			removed, target := 0, len(o.all)/2
			for _, tx := range txs {
				if removed < target && o.all[tx.Hash()] != nil {
					p.all.Remove(tx.Hash())
					delete(o.all, tx.Hash())
					removed++
				}
			}
			p.Removed(removed)
			o.stales += int64(removed)
			if int(o.stales) > (len(o.urgent)+len(o.floating))/4 {
				o.reheap()
			}
			checkPriceOracle(t, p, o)
			p.all.Clear()
			p.Reheap()
			o.all = make(map[common.Hash]*types.Transaction)
			o.reheap()
			checkPriceOracle(t, p, o)
			probe := comparisonTransaction(20_000, 1, 1, nil)
			if p.Underpriced(probe) || o.underpriced(probe) {
				t.Fatal("empty pool rejected a price")
			}
			if dropped, ok := p.Discard(1); ok || len(dropped) != 0 {
				t.Fatal("empty pool satisfied discard")
			}
		})
	}
}
