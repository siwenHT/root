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

func TestElfomoBookAgainstRealBSCContractsAndPostHandle(t *testing.T) {
	raw, err := os.ReadFile("testdata/elfomo-state.json")
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
		Book string `json:"book"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	cfg := *params.TestChainConfig
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
	got, err := readElfomoBook(caller, elfomoTokens)
	if err != nil {
		t.Fatal(err)
	}
	if hexutil.Encode(got) != fixture.Book {
		t.Fatal("real helper book mismatch")
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
	args := GetElfomoBookArgs{ParentHash: x.parent.Hash().Hex(), RequestID: identity.Identity, PostHandle: child, BlockEnv: env, BudgetMicros: 5_000_000}
	result, err := (&ArbAPI{svc: svc}).GetElfomoBook(args)
	if err != nil {
		t.Fatal(err)
	}
	expected := common.FromHex(fixture.Book)
	// The first three ABI words are book offset, block number and timestamp.
	copy(expected[32:64], common.LeftPadBytes(new(big.Int).SetUint64(x.parent.Number.Uint64()+1).Bytes(), 32))
	if result.Data != hexutil.Encode(expected) || result.Boot != svc.boot || result.EnvHash == "" {
		t.Fatal("wrong post-handle book or environment")
	}
	args.ParentHash = common.Hash{}.Hex()
	if _, err := (&ArbAPI{svc: svc}).GetElfomoBook(args); err == nil {
		t.Fatal("accepted foreign parent")
	}
	if len(x.base.GetCode(elfomoHelper)) != 0 {
		t.Fatal("reader changed canonical parent")
	}
}

func TestElfomoBookRejectsEmptyRequest(t *testing.T) {
	if _, err := (&ArbAPI{}).GetElfomoBook(GetElfomoBookArgs{}); err == nil {
		t.Fatal("accepted empty request")
	}
}

func TestElfomoResolveTokens(t *testing.T) {
	def, err := resolveElfomoTokens("", "")
	if err != nil || def != elfomoTokens {
		t.Fatalf("expected legacy default pair, got %v err=%v", def, err)
	}
	antb := "0x38dd26788cf2d901e3abf04efe3007d459d0fc53"
	usdt := "0x55d398326f99059ff775485246999027b3197955"
	explicit, err := resolveElfomoTokens(antb, usdt)
	if err != nil || explicit[0] != common.HexToAddress(antb) || explicit[1] != common.HexToAddress(usdt) {
		t.Fatalf("expected explicit pair, got %v err=%v", explicit, err)
	}
	for _, bad := range [][2]string{
		{antb, ""},
		{"", usdt},
		{"0x0000000000000000000000000000000000000000", usdt},
		{antb, antb},
		{usdt, antb},
		{"not-hex", usdt},
	} {
		if _, err := resolveElfomoTokens(bad[0], bad[1]); err == nil {
			t.Fatalf("expected invalid pair error for %v", bad)
		}
	}
}
