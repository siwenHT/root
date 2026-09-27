package eth

import (
	"bytes"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

var pancakeCLManager = common.HexToAddress("0xa0ffb9c1ce1fe56963b0321b32e7a0302114058b")
var pancakeCLCodeHash = common.HexToHash("0x3caf72836cb6603c6af03bba1578ec70ece8c3e5b1d0ef73667b5fbd74b02a0f")

// Validate metadata returned by the same parent/post-target StateDB as slot0.
// A caller hint may confirm spacing but must never override the hashed key.
func verifiedInfinityKey(manager common.Address, id common.Hash, raw []byte, hint int64) (*arb.InfinityPoolKey, int64, error) {
	if len(raw) != 192 || crypto.Keccak256Hash(raw) != id {
		return nil, 0, errors.New("arb: infinity pool key mismatch")
	}
	key, err := arb.DecodeInfinityPoolKey(raw)
	if err != nil {
		return nil, 0, err
	}
	if common.BytesToAddress(key.PoolManager[:]) != manager || bytes.Compare(key.Currency0[:], key.Currency1[:]) >= 0 {
		return nil, 0, errors.New("arb: infinity invalid key identity")
	}
	params := new(big.Int).SetBytes(key.Parameters[:])
	spacing := int64(new(big.Int).Rsh(params, 16).Uint64() & 0xffffff)
	if spacing <= 0 || spacing > 32767 || (hint != 0 && hint != spacing) {
		return nil, 0, errors.New("arb: infinity tick spacing mismatch")
	}
	return key, spacing, nil
}
