package eth

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

func TestCanonicalPrefixRetainsRevertedNonceAndFee(t *testing.T) {
	key, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	sender := crypto.PubkeyToAddress(key.PublicKey)
	reverter := common.HexToAddress("0x1000")
	receiver := common.HexToAddress("0x2000")
	genDB := rawdb.NewMemoryDatabase()
	gen := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{
		sender:   {Balance: new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)},
		reverter: {Code: common.FromHex("60006000fd")},
	}}
	genesis := gen.MustCommit(genDB, triedb.NewDatabase(genDB, triedb.HashDefaults))
	blocks, receipts := core.GenerateChain(gen.Config, genesis, ethash.NewFaker(), genDB, 1, func(_ int, b *core.BlockGen) {
		for i, to := range []common.Address{reverter, receiver} {
			tx, err := types.SignTx(types.NewTransaction(uint64(i), to, big.NewInt(1000), 100000, b.BaseFee(), nil), types.LatestSigner(gen.Config), key)
			if err != nil {
				t.Fatal(err)
			}
			b.AddTx(tx)
		}
	})
	bc, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), gen, ethash.NewFaker(), core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Stop()
	if _, err := bc.InsertChain(blocks); err != nil {
		t.Fatal(err)
	}
	base, err := bc.StateAt(genesis.Root())
	if err != nil {
		t.Fatal(err)
	}
	x := newTargetExecutor(bc, genesis.Header(), base)
	res, err := x.executeCanonicalPrefix(blocks[0], 2, receipts[0], newReadBudget(1000000, 0))
	if err != nil || !res.Completed || res.Outcomes[0].Class.Status != arb.StatusReverted || res.Outcomes[1].Class.Status != arb.StatusSuccess {
		t.Fatalf("canonical reverted prefix did not complete: result=%+v error=%v", res, err)
	}
	native, err := bc.StateAt(blocks[0].Root())
	if err != nil {
		t.Fatal(err)
	}
	if res.PostState.GetNonce(sender) != 2 || res.PostState.GetBalance(sender).Cmp(native.GetBalance(sender)) != 0 || !res.PostState.GetBalance(reverter).IsZero() {
		t.Fatal("canonical nonce, fees or reverted value differ from native processing")
	}
	if base.GetNonce(sender) != 0 {
		t.Fatal("borrowed parent was mutated")
	}
	bad := append(types.Receipts(nil), receipts[0]...)
	changed := *bad[0]
	changed.GasUsed++
	bad[0] = &changed
	mismatch, err := x.executeCanonicalPrefix(blocks[0], 2, bad, newReadBudget(1000000, 0))
	if err != nil || mismatch.Completed || mismatch.PostState != nil {
		t.Fatal("receipt mismatch produced a usable state")
	}

	svc, err := (&Ethereum{blockchain: bc}).newArbService(arbServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Start(); err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()
	api := &ArbAPI{svc: svc}
	pin, err := api.PinState(PinStateArgs{RequestID: arb.RandomID32(), ParentHash: genesis.Hash().Hex(), TTLMicros: 10000000})
	if err != nil {
		t.Fatal(err)
	}
	job, err := api.ReplayCanonicalPrefix(ReplayCanonicalPrefixArgs{RequestID: arb.RandomID32(), Stamp: arb.RandomID32(), ParentHandle: pin.ParentHandle, BlockHash: blocks[0].Hash().Hex(), StopIndex: 2, BudgetMicros: 1000000})
	if err != nil {
		t.Fatal(err)
	}
	var view *JobResult
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		view, err = api.GetJob(JobArgs{JobID: job.JobID, Boot: pin.Boot})
		if err != nil {
			t.Fatal(err)
		}
		if view.Status != "Queued" && view.Status != "Running" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if view.Status != "Succeeded" || !view.Completeness || view.PostHandleOrNull == nil || view.ResultKind != "canonical_prefix" {
		t.Fatalf("canonical job failed: %+v", view)
	}
	account, err := api.GetAccountState(GetAccountStateArgs{RequestID: arb.RandomID32(), HandleID: *view.PostHandleOrNull, Address: common.Bytes2Hex(sender.Bytes())})
	if err == nil {
		t.Fatal("noncanonical address shape accepted")
	}
	account, err = api.GetAccountState(GetAccountStateArgs{RequestID: arb.RandomID32(), HandleID: *view.PostHandleOrNull, Address: "0x" + common.Bytes2Hex(sender.Bytes())})
	if err != nil || account.Nonce != 2 || account.Balance != native.GetBalance(sender).ToBig().String() {
		t.Fatalf("pinned account state differs: %+v, %v", account, err)
	}
}
