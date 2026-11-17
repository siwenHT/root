package eth

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
	"math/big"
	"testing"
	"time"
)

func TestLiquidFiReadsThePostHandleAndChild(t *testing.T) {
	x, _, chain := prefixFixture(t)
	defer chain.Stop()
	svc, err := (&Ethereum{blockchain: chain}).newArbService(arbServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	identity := arb.HandleIdentity{OwnerSession: arb.RandomID32(), NodeBootID: svc.boot, Identity: arb.RandomID32()}
	identity.OwnerSession = identity.Identity
	parent, _, err := svc.pinParent(identity, x.parent.Hash(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.releaseParent(parent)
	post, header, release, err := svc.borrowBase(parent, identity)
	if err != nil {
		t.Fatal(err)
	}
	// The real reader calls these contracts. The bytecode fixture exposes the
	// selected state's storage and EVM NUMBER rather than simulating the reader.
	entry := common.FromHex("0x60003560e01c637ce914111460005773" + liquidFiVault.Hex()[2:] + "60005260206000f3")
	entry[13] = byte(len(entry))
	entry = append(entry, common.FromHex("0x5b73"+liquidFiPricing.Hex()[2:]+"60005260206000f3")...)

	// Both global config/cumulatives return storage[0] and NUMBER.
	data := common.FromHex("0x6000546000524360205260406000f3")
	post.SetCode(liquidFiEntry, entry, 0)
	post.SetCode(liquidFiPricing, data, 0)
	for _, token := range liquidFiTokens {
		post.SetCode(token, common.FromHex("0x60005460005260206000f3"), 0)
		post.SetState(token, common.Hash{}, common.BigToHash(big.NewInt(73)))
	}
	post.SetState(liquidFiPricing, common.Hash{}, common.BigToHash(big.NewInt(42)))
	packed := common.HexToHash("0x123456")
	post.SetState(liquidFiPricing, common.HexToHash("0xd61fc50f5da629347f6bd9d3d325fba6241c04ec01b687e5d89c356860cfe3f6"), packed)
	blockRoot := crypto.Keccak256Hash(packed.Bytes(), common.HexToHash("0x8759f18911d145bf318ed8280c3612d855b1eb13a1ec427ab54ed53d55d77d0d").Bytes())
	blockNumber, _ := new(big.Int).SetString(validEnvForExecutor(x).Number, 10)
	blockSlot := crypto.Keccak256Hash(common.LeftPadBytes(blockNumber.Bytes(), 32), blockRoot.Bytes())
	post.SetState(liquidFiPricing, blockSlot, common.BigToHash(big.NewInt(91)))
	child := svc.registerPostChild(parent, identity, post, header)
	release()
	defer svc.releaseParent(child)
	args := GetLiquidFiStateArgs{ParentHash: x.parent.Hash().Hex(), RequestID: identity.Identity, PostHandle: child, BlockEnv: validEnvForExecutor(x), BudgetMicros: 5_000_000}
	result, err := (&ArbAPI{svc: svc}).GetLiquidFiState(args)
	if err != nil {
		t.Fatal(err)
	}
	if result.BlockInput != common.BigToHash(big.NewInt(91)).Hex() || result.Packed != packed.Hex() || result.Block != args.BlockEnv.Number || result.Boot != svc.boot || result.ParentHash != args.ParentHash {
		t.Fatalf("wrong post state/context: %+v", result)
	}
	if new(big.Int).SetBytes(common.FromHex(result.Cumulative)[:32]).Uint64() != 42 || new(big.Int).SetBytes(common.FromHex(result.Cumulative)[32:]).String() != args.BlockEnv.Number {
		t.Fatal("reader used the wrong EVM state or block")
	}
	if new(big.Int).SetBytes(common.FromHex(result.Balances[0])).Uint64() != 73 {
		t.Fatal("reader lost post-target token balances")
	}
	if len(x.base.GetCode(liquidFiEntry)) != 0 {
		t.Fatal("post state leaked into parent")
	}
	args.BlockEnv.ParentHash = common.Hash{}.Hex()
	if _, err := (&ArbAPI{svc: svc}).GetLiquidFiState(args); err == nil {
		t.Fatal("accepted a different child parent")
	}
	args.BlockEnv = validEnvForExecutor(x)
	args.ParentHash = common.Hash{}.Hex()
	if _, err := (&ArbAPI{svc: svc}).GetLiquidFiState(args); err == nil {
		t.Fatal("accepted a different requested parent")
	}
}
