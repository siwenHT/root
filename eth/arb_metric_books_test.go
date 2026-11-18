package eth

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/eth/arb"
)

func TestMetricBooksUsePostStateChildAndIsolatedCalls(t *testing.T) {
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
	pool := common.HexToAddress("0x0000000000000000000000000000000000000033")
	// Increment storage, then return it with the actual child and transaction context.
	code := common.FromHex("0x6000546001016000556000546000524360205242604052326060523a60805260a06000f3")
	for _, address := range []common.Address{pool, metricDataProvider} {
		post.SetCode(address, code, 0)
		post.SetState(address, common.Hash{}, common.BigToHash(big.NewInt(42)))
	}
	child := svc.registerPostChild(parent, identity, post, header)
	release()
	defer svc.releaseParent(child)
	args := GetMetricBooksArgs{RequestID: identity.Identity, PostHandle: child, BlockEnv: validEnvForExecutor(x),
		Sender: "0x0000000000000000000000000000000000000012", GasPrice: "50000000",
		BudgetMicros: 5_000_000, Pools: []string{pool.Hex()}}
	api := &ArbAPI{svc: svc}
	result, err := api.GetMetricBooks(args)
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range []string{result.Books[0].Immutable, result.Books[0].Data, result.Books[0].Slot0} {
		raw, err := hexutil.Decode(value)
		if err != nil || len(raw) != 160 {
			t.Fatalf("bad EVM return: %s (%v)", value, err)
		}
		values := make([]*big.Int, 5)
		for i := range values {
			values[i] = new(big.Int).SetBytes(raw[i*32 : (i+1)*32])
		}
		expectedOrigin := common.HexToAddress(args.Sender)
		if index == 1 {
			expectedOrigin = common.Address{}
		}
		if values[0].Uint64() != 43 || values[1].String() != args.BlockEnv.Number || values[2].String() != args.BlockEnv.TimestampSeconds ||
			common.BigToAddress(values[3]) != expectedOrigin || values[4].String() != args.GasPrice {
			t.Fatalf("wrong post state or child context: %v", values)
		}
	}
	if post.GetState(pool, common.Hash{}) != common.BigToHash(big.NewInt(42)) ||
		post.GetState(metricDataProvider, common.Hash{}) != common.BigToHash(big.NewInt(42)) || len(x.base.GetCode(pool)) != 0 {
		t.Fatal("Metric reads changed the borrowed post state or parent")
	}
	if result.Boot != svc.boot || result.ParentHash != x.parent.Hash().Hex() {
		t.Fatal("lost state identity")
	}
	args.BlockEnv.ParentHash = common.Hash{}.Hex()
	if _, err := api.GetMetricBooks(args); err == nil {
		t.Fatal("accepted a different parent")
	}
	args.BlockEnv = validEnvForExecutor(x)
	args.BudgetMicros = 1
	if _, err := api.GetMetricBooks(args); err == nil {
		t.Fatal("accepted an expired Metric reader budget")
	}
	args.BudgetMicros = 5_000_000
	args.Pools = []string{"bad-pool"}
	if _, err := api.GetMetricBooks(args); err == nil {
		t.Fatal("accepted malformed external pool address")
	}
}

func TestMetricLargeDepthKeepsOtherViewGasCaps(t *testing.T) {
	x, _, chain := prefixFixture(t)
	defer chain.Stop()
	caller := newPoolCallerOver(chain, x.base, x.parent, newReadBudget(defaultMaxReads, time.Second))
	deep := common.HexToAddress("0x06c24ec733b33fb9f57f4c2ca1f3a4fab16b686e")
	ordinary := common.HexToAddress("0x213de872f182ef96ae7025e13224237acd4bc562")
	// Return 32 bytes. Only the two-argument depth entry expands memory by 1 MiB,
	// which costs more than viewGas and less than the BNCB depth allowance.
	lens := common.FromHex("0x60443614600c5760206000f35b6001621000005260206000f3")
	// Return getImmutables-shaped words with 100 or 26 bins starting at zero.
	caller.wrapped.SetCode(deep, common.FromHex("0x6063610140526101606000f3"), 0)
	caller.wrapped.SetCode(ordinary, common.FromHex("0x6019610140526101606000f3"), 0)
	caller.wrapped.SetCode(metricDataProvider, lens, 0)
	book, err := readMetricBook(caller, deep)
	if err != nil || book.Data == "" || book.Slot0 == "" || book.Immutable == "" {
		t.Fatalf("large depth read failed: %+v, %v", book, err)
	}
	if _, err := readMetricBook(caller, ordinary); err != ErrViewCallReverted {
		t.Fatalf("ordinary depth must keep the existing gas cap, got %v", err)
	}
}
