package eth

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/eth/arb"
)

func TestPrismBooksUsePostStateAndPreparedChild(t *testing.T) {
	x, _, chain := prefixFixture(t)
	defer chain.Stop()
	node := &Ethereum{blockchain: chain}
	svc, err := node.newArbService(arbServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	identity := arb.HandleIdentity{OwnerSession: arb.RandomID32(), NodeBootID: svc.boot, Identity: arb.RandomID32()}
	parent, _, err := svc.pinParent(identity, x.parent.Hash(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.releaseParent(parent)
	post, header, release, err := svc.borrowBase(parent, identity)
	if err != nil {
		t.Fatal(err)
	}
	// A real EVM getter returns its post-target storage, NUMBER and TIMESTAMP.
	code := common.FromHex("0x600054600052436020524260405260606000f3")
	post.SetCode(prismRouter, code, 0)
	post.SetState(prismRouter, common.Hash{}, common.BigToHash(big.NewInt(42)))
	child := svc.registerPostChild(parent, identity, post, header)
	release()
	defer svc.releaseParent(child)
	args := GetPrismBooksArgs{RequestID: identity.Identity, PostHandle: child, BlockEnv: validEnvForExecutor(x),
		Adapter: "0x0000000000000000000000000000000000000011", Sender: "0x0000000000000000000000000000000000000012",
		GasPrice: "50000000", BudgetMicros: 5_000_000,
		Pairs: []PrismPair{{Token0: "0x0000000000000000000000000000000000000001", Token1: "0x0000000000000000000000000000000000000002"}}}
	api := &ArbAPI{svc: svc}
	result, err := api.GetPrismBooks(args)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hexutil.Decode(result.Books[0].Data)
	if err != nil || len(raw) != 96 || new(big.Int).SetBytes(raw[:32]).Uint64() != 42 ||
		new(big.Int).SetBytes(raw[32:64]).String() != args.BlockEnv.Number ||
		new(big.Int).SetBytes(raw[64:]).String() != args.BlockEnv.TimestampSeconds {
		t.Fatalf("wrong post-state/environment: %+v (%v)", result, err)
	}
	if result.Boot != svc.boot || result.ParentHash != x.parent.Hash().Hex() {
		t.Fatal("lost state identity")
	}
	if len(x.base.GetCode(prismRouter)) != 0 || post.GetState(prismRouter, common.Hash{}) != common.BigToHash(big.NewInt(42)) {
		t.Fatal("read mutated the parent or post state")
	}
	args.BlockEnv.ParentHash = common.Hash{}.Hex()
	if _, err := api.GetPrismBooks(args); err == nil {
		t.Fatal("accepted a different parent environment")
	}
}
