package eth

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

// Removed/replaced transactions disappear from the pool hash index. A hit must
// also match the requested sender and nonce; it never substitutes another tx.
func targetSlotByHash(boot string, args TargetSlotArgs, tx *types.Transaction, signer types.Signer) (*TargetSlotResult, error) {
	result := &TargetSlotResult{Boot: boot}
	if tx == nil || tx.Hash() != common.HexToHash(args.TargetHash) || tx.Nonce() != args.Nonce {
		return result, nil
	}
	sender, err := types.Sender(signer, tx)
	if err != nil || sender != common.HexToAddress(args.Sender) {
		return result, nil
	}
	raw, err := tx.MarshalBinary()
	if err != nil {
		return nil, err
	}
	encoded, hash := hexutil.Encode(raw), tx.Hash().Hex()
	result.CurrentRawOrNil, result.CurrentHashOrNil = &encoded, &hash
	return result, nil
}
