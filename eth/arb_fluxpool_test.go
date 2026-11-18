package eth

import (
	"encoding/json"
	"math/big"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/arb"
	"github.com/ethereum/go-ethereum/params"
)

func TestFluxPoolStateAgainstRealBSCContractsAndPostHandle(t *testing.T) {
	raw, err := os.ReadFile("testdata/fluxpool-state.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Timestamp  uint64 `json:"timestamp"`
		Block      uint64 `json:"block"`
		Allocation map[common.Address]struct {
			Code    hexutil.Bytes               `json:"code"`
			Storage map[common.Hash]common.Hash `json:"storage"`
		} `json:"allocation"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	cfg := *params.TestChainConfig
	cfg.ChainID = big.NewInt(56)
	zero := uint64(0)
	cfg.ShanghaiTime = &zero
	cfg.CancunTime = &zero
	cfg.TerminalTotalDifficulty = big.NewInt(0)
	cfg.BlobScheduleConfig = params.DefaultBlobSchedule
	genesis := &core.Genesis{Config: &cfg, Difficulty: big.NewInt(0), Timestamp: fixture.Timestamp - 1, Alloc: types.GenesisAlloc{}}
	db := rawdb.NewMemoryDatabase()
	chain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	parentHeader := chain.CurrentBlock()
	parentBase, err := chain.StateAt(parentHeader)
	if err != nil {
		t.Fatal(err)
	}
	x := newTargetExecutor(chain, parentHeader, parentBase)
	base := x.base.Copy()
	for address, account := range fixture.Allocation {
		base.SetCode(address, account.Code, 0)
		for slot, value := range account.Storage {
			base.SetState(address, slot, value)
		}
	}
	header := types.CopyHeader(x.parent)
	header.Time = fixture.Timestamp
	header.Number = new(big.Int).SetUint64(fixture.Block)
	caller := newPoolCallerOver(chain, base, header, newReadBudget(defaultMaxReads, 5*time.Second))
	got, err := readFluxPoolState(caller)
	if err != nil {
		t.Fatal(err)
	}
	if hexutil.Encode(got) != fixture.Data {
		t.Fatal("real FluxPool state mismatch")
	}
	svc, err := (&Ethereum{blockchain: chain}).newArbService(arbServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	identity := arb.HandleIdentity{OwnerSession: arb.RandomID32(), NodeBootID: svc.boot}
	identity.Identity = identity.OwnerSession
	parent, _, err := svc.pinParent(identity, x.parent.Hash(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.releaseParent(parent)
	child := svc.registerPostChild(parent, identity, base, x.parent)
	defer svc.releaseParent(child)
	env := validEnvForExecutor(x)
	env.TimestampSeconds = strconv.FormatUint(fixture.Timestamp, 10)
	env.TimestampMs = strconv.FormatUint(fixture.Timestamp*1000, 10)
	env, err = x.prepareBlockEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	args := GetFluxPoolStateArgs{ParentHash: x.parent.Hash().Hex(), RequestID: identity.Identity, PostHandle: child, BlockEnv: env, BudgetMicros: 5_000_000}
	result, err := (&ArbAPI{svc: svc}).GetFluxPoolState(args)
	if err != nil {
		t.Fatal(err)
	}
	expected := common.FromHex(fixture.Data)
	// The first two state words bind the reader to its prepared block and time.
	copy(expected[:32], common.LeftPadBytes(new(big.Int).SetUint64(x.parent.Number.Uint64()+1).Bytes(), 32))
	copy(expected[32:64], common.LeftPadBytes(new(big.Int).SetUint64(fixture.Timestamp).Bytes(), 32))
	if result.Data != hexutil.Encode(expected) || result.Boot != svc.boot || result.EnvHash == "" {
		t.Fatal("wrong post-handle state or environment")
	}
	args.ParentHash = common.Hash{}.Hex()
	if _, err := (&ArbAPI{svc: svc}).GetFluxPoolState(args); err == nil {
		t.Fatal("accepted foreign parent")
	}
	if len(x.base.GetCode(fluxPoolRouter)) != 0 {
		t.Fatal("reader changed canonical parent")
	}
}

func TestFluxPoolStateRejectsEmptyRequest(t *testing.T) {
	if _, err := (&ArbAPI{}).GetFluxPoolState(GetFluxPoolStateArgs{}); err == nil {
		t.Fatal("accepted empty request")
	}
}
