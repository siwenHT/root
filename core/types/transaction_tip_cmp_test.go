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

package types

import (
	"fmt"
	"math/big"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"
)

// effectiveGasTipCmpBeforeZeroBaseFee is the comparator before the zero-base-fee
// fast path. Keep the uint256 conversion and ignored errors as the oracle, even
// for malformed fee caps which cannot enter the pool.
func effectiveGasTipCmpBeforeZeroBaseFee(a, b *Transaction, baseFee *uint256.Int) int {
	if baseFee == nil {
		return a.GasTipCapCmp(b)
	}
	var aTip, bTip uint256.Int
	a.calcEffectiveGasTip(&aTip, baseFee)
	b.calcEffectiveGasTip(&bTip, baseFee)
	return aTip.Cmp(&bTip)
}

func tipCmpTransaction(kind byte, tip, fee *big.Int) *Transaction {
	switch kind {
	case LegacyTxType:
		return NewTx(&LegacyTx{GasPrice: fee})
	case AccessListTxType:
		return NewTx(&AccessListTx{GasPrice: fee})
	case DynamicFeeTxType:
		return NewTx(&DynamicFeeTx{GasTipCap: tip, GasFeeCap: fee})
	case BlobTxType:
		return NewTx(&BlobTx{GasTipCap: uint256.MustFromBig(tip), GasFeeCap: uint256.MustFromBig(fee)})
	case SetCodeTxType:
		return NewTx(&SetCodeTx{GasTipCap: uint256.MustFromBig(tip), GasFeeCap: uint256.MustFromBig(fee)})
	default:
		panic("unknown transaction type")
	}
}

func TestEffectiveGasTipCmpDifferential(t *testing.T) {
	limit := new(big.Int).Lsh(big.NewInt(1), 256)
	values := []*big.Int{
		big.NewInt(0), big.NewInt(1), big.NewInt(3_000_000_000),
		new(big.Int).Lsh(big.NewInt(1), 255),
		new(big.Int).Sub(new(big.Int).Set(limit), big.NewInt(1)),
		limit, new(big.Int).Add(new(big.Int).Set(limit), big.NewInt(1)),
		big.NewInt(-1), new(big.Int).Neg(new(big.Int).Set(limit)),
	}
	var txs []*Transaction
	for _, kind := range []byte{LegacyTxType, AccessListTxType, DynamicFeeTxType, BlobTxType, SetCodeTxType} {
		for _, tip := range values {
			for _, fee := range values {
				if (kind == BlobTxType || kind == SetCodeTxType) &&
					(tip.Sign() < 0 || fee.Sign() < 0 || tip.BitLen() > 256 || fee.BitLen() > 256) {
					continue
				}
				txs = append(txs, tipCmpTransaction(kind, tip, fee))
			}
		}
	}
	for _, baseFee := range []*uint256.Int{nil, new(uint256.Int), uint256.NewInt(1), uint256.NewInt(3_000_000_000), new(uint256.Int).SetAllOne()} {
		for i, a := range txs {
			for j, b := range txs {
				got := a.EffectiveGasTipCmp(b, baseFee)
				if want := effectiveGasTipCmpBeforeZeroBaseFee(a, b, baseFee); got != want {
					t.Fatalf("base=%v pair=%d/%d types=%d/%d: have %d, want %d", baseFee, i, j, a.Type(), b.Type(), got, want)
				}
			}
		}
	}
}

func TestEffectiveGasTipCmpZeroBaseFee(t *testing.T) {
	// Zero must use min(tip, fee), whereas nil keeps the historical tip-only path.
	a := tipCmpTransaction(DynamicFeeTxType, big.NewInt(9), big.NewInt(3))
	b := tipCmpTransaction(DynamicFeeTxType, big.NewInt(5), big.NewInt(7))
	baseFee := new(uint256.Int)
	if got := a.EffectiveGasTipCmp(b, baseFee); got != -1 {
		t.Fatalf("zero base fee: have %d, want -1", got)
	}
	if got := a.EffectiveGasTipCmp(b, nil); got != 1 {
		t.Fatalf("nil base fee: have %d, want 1", got)
	}
	if a.GasTipCap().Int64() != 9 || a.GasFeeCap().Int64() != 3 ||
		b.GasTipCap().Int64() != 5 || b.GasFeeCap().Int64() != 7 || !baseFee.IsZero() {
		t.Fatal("comparison mutated fee caps or base fee")
	}
}

func FuzzEffectiveGasTipCmpDifferential(f *testing.F) {
	f.Add([]byte{0}, []byte{1}, []byte{2}, []byte{3}, uint8(0))
	f.Add([]byte{9}, []byte{3}, []byte{5}, []byte{7}, uint8(2))
	f.Add(make([]byte, 33), []byte{0xff}, []byte{1}, []byte{0}, uint8(4))
	f.Fuzz(func(t *testing.T, aTip, aFee, bTip, bFee []byte, kind uint8) {
		// Include overflow and negative big.Int values without unbounded fuzz inputs.
		caps := make([]*big.Int, 4)
		for i, data := range [][]byte{aTip, aFee, bTip, bFee} {
			if len(data) > 34 {
				data = data[:34]
			}
			caps[i] = new(big.Int).SetBytes(data)
			if len(data) > 0 && data[0]&1 != 0 {
				caps[i].Neg(caps[i])
			}
		}
		a := tipCmpTransaction(kind%3, caps[0], caps[1])
		b := tipCmpTransaction((kind+1)%3, caps[2], caps[3])
		for _, base := range []*uint256.Int{nil, new(uint256.Int), uint256.NewInt(uint64(kind))} {
			if got, want := a.EffectiveGasTipCmp(b, base), effectiveGasTipCmpBeforeZeroBaseFee(a, b, base); got != want {
				t.Fatalf("base=%v: have %d, want %d", base, got, want)
			}
		}
	})
}

var tipCmpBenchmarkResult int

func BenchmarkEffectiveGasTipCmpBaseFee(b *testing.B) {
	for _, kind := range []byte{LegacyTxType, DynamicFeeTxType, SetCodeTxType} {
		for _, base := range []uint64{0, 1_000_000_000} {
			b.Run(fmt.Sprintf("type%d/base%d", kind, base), func(b *testing.B) {
				rng := rand.New(rand.NewSource(24))
				txs := make([]*Transaction, 256)
				for i := range txs {
					tip := big.NewInt(rng.Int63n(5_000_000_000))
					fee := new(big.Int).Add(tip, big.NewInt(1_000_000_000))
					txs[i] = tipCmpTransaction(kind, tip, fee)
				}
				baseFee := uint256.NewInt(base)
				b.ReportAllocs()
				i := 0
				for b.Loop() {
					tipCmpBenchmarkResult = txs[i&255].EffectiveGasTipCmp(txs[(i+1)&255], baseFee)
					i++
				}
			})
		}
	}
}
