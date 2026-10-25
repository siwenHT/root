package types

import "github.com/holiman/uint256"

// GasTipCmpPath describes the branch actually taken, not inferred eligibility.
type GasTipCmpPath uint8

const (
	GasTipCmpNil GasTipCmpPath = iota
	GasTipCmpZeroFast
	GasTipCmpZeroFallback
	GasTipCmpNonzero
)

// EffectiveGasTipCmpWithPath mirrors EffectiveGasTipCmp for sampled diagnostics.
// Keep the normal comparator unchanged: no counter or sampling branch is added
// to its hot path. Differential tests cover this intentional duplication.
func (tx *Transaction) EffectiveGasTipCmpWithPath(other *Transaction, baseFee *uint256.Int) (int, GasTipCmpPath) {
	if baseFee == nil {
		return tx.GasTipCapCmp(other), GasTipCmpNil
	}
	path := GasTipCmpNonzero
	if baseFee.IsZero() {
		path = GasTipCmpZeroFallback
		txTip, txFee := tx.inner.gasTipCap(), tx.inner.gasFeeCap()
		otherTip, otherFee := other.inner.gasTipCap(), other.inner.gasFeeCap()
		if txTip.Sign() >= 0 && txFee.Sign() >= 0 && otherTip.Sign() >= 0 && otherFee.Sign() >= 0 &&
			txTip.BitLen() <= 256 && txFee.BitLen() <= 256 && otherTip.BitLen() <= 256 && otherFee.BitLen() <= 256 {
			if txTip.Cmp(txFee) > 0 {
				txTip = txFee
			}
			if otherTip.Cmp(otherFee) > 0 {
				otherTip = otherFee
			}
			return txTip.Cmp(otherTip), GasTipCmpZeroFast
		}
	}
	txTip, otherTip := new(uint256.Int), new(uint256.Int)
	tx.calcEffectiveGasTip(txTip, baseFee)
	other.calcEffectiveGasTip(otherTip, baseFee)
	return txTip.Cmp(otherTip), path
}
