package types

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
)

func TestDiagnosticGasTipBranches(t *testing.T) {
	values := []*big.Int{big.NewInt(-1), new(big.Int), big.NewInt(1), big.NewInt(100), new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)), new(big.Int).Lsh(big.NewInt(1), 256)}
	var txs []*Transaction
	for _, kind := range []byte{LegacyTxType, AccessListTxType, DynamicFeeTxType, BlobTxType, SetCodeTxType} {
		for _, fee := range values {
			for _, tip := range values {
				if (kind == BlobTxType || kind == SetCodeTxType) && (fee.Sign() < 0 || tip.Sign() < 0 || fee.BitLen() > 256 || tip.BitLen() > 256) {
					continue
				}
				txs = append(txs, tipCmpTransaction(kind, tip, fee))
			}
		}
	}
	paths := make(map[GasTipCmpPath]int)
	for _, base := range []*uint256.Int{nil, new(uint256.Int), uint256.NewInt(3)} {
		for _, a := range txs {
			for _, b := range txs {
				got, path := a.EffectiveGasTipCmpWithPath(b, base)
				if want := a.EffectiveGasTipCmp(b, base); got != want {
					t.Fatalf("result differs: base=%v path=%v got=%d want=%d", base, path, got, want)
				}
				wantPath := GasTipCmpNonzero
				if base == nil {
					wantPath = GasTipCmpNil
				} else if base.IsZero() {
					wantPath = GasTipCmpZeroFast
					for _, value := range []*big.Int{a.GasFeeCap(), a.GasTipCap(), b.GasFeeCap(), b.GasTipCap()} {
						if value.Sign() < 0 || value.BitLen() > 256 {
							wantPath = GasTipCmpZeroFallback
						}
					}
				}
				if path != wantPath {
					t.Fatalf("wrong branch %v want %v", path, wantPath)
				}
				paths[path]++
			}
		}
	}
	for _, path := range []GasTipCmpPath{GasTipCmpNil, GasTipCmpZeroFast, GasTipCmpZeroFallback, GasTipCmpNonzero} {
		if paths[path] == 0 {
			t.Fatalf("unexercised path %v", path)
		}
	}
}
