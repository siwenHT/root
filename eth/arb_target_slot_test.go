package eth

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"math/big"
	"testing"
)

func TestTargetSlotByHashRejectsMissingReplacementAndWrongIdentity(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := types.LatestSignerForChainID(big.NewInt(56))
	tx, err := types.SignTx(types.NewTransaction(7, common.Address{1}, big.NewInt(0), 21000, big.NewInt(1), nil), signer, key)
	if err != nil {
		t.Fatal(err)
	}
	args := TargetSlotArgs{TargetHash: tx.Hash().Hex(), Sender: crypto.PubkeyToAddress(key.PublicKey).Hex(), Nonce: 7}
	hit, err := targetSlotByHash("boot", args, tx, signer)
	if err != nil || hit.CurrentRawOrNil == nil || *hit.CurrentHashOrNil != tx.Hash().Hex() {
		t.Fatal("lost exact pool tx", err)
	}
	for mode := 0; mode < 4; mode++ {
		a, candidate := args, tx
		switch mode {
		case 0:
			candidate = nil
		case 1:
			a.TargetHash = common.Hash{9}.Hex()
		case 2:
			a.Nonce++
		case 3:
			a.Sender = common.Address{9}.Hex()
		}
		miss, err := targetSlotByHash("boot", a, candidate, signer)
		if err != nil || miss.CurrentRawOrNil != nil || miss.CurrentHashOrNil != nil {
			t.Fatalf("mode %d accepted wrong target", mode)
		}
	}
}
