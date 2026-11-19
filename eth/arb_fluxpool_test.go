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
	checkPreparedPoolStateFixture(t, "testdata/fluxpool-state.json", fluxPoolRouter, true, "", readFluxPoolState, (*ArbAPI).GetFluxPoolState)
}

func TestFluxPoolBncbStateAgainstRealBSCContractsAndPostHandle(t *testing.T) {
	read := func(c *poolCaller) ([]byte, error) { return readFluxPool(c, fluxPoolBncb, fluxPoolBncbTokens) }
	get := func(api *ArbAPI, args GetFluxPoolStateArgs) (*GetFluxPoolStateResult, error) {
		args.PoolID = fluxPoolBncb.Hex()
		return api.GetFluxPoolState(args)
	}
	checkPreparedPoolStateFixture(t, "testdata/fluxpool-bncb-state.json", fluxPoolRouter, true, "", read, get)
}

func TestFluxPoolStateRejectsUnknownPoolID(t *testing.T) {
	for _, id := range []string{"bad", common.Hash{}.Hex()} {
		if _, err := (&ArbAPI{}).GetFluxPoolState(GetFluxPoolStateArgs{PoolID: id}); err == nil {
			t.Fatal("accepted unsupported FluxPool id")
		}
	}
}

func TestSquadBinStateAgainstRealBSCContractsAndPostHandle(t *testing.T) {
	checkPreparedPoolStateFixture(t, "testdata/squad-bin-state.json", squadBinManager, false, "", readSquadBinState, func(api *ArbAPI, args GetFluxPoolStateArgs) (*GetFluxPoolStateResult, error) {
		return api.GetSquadBinState(GetSquadBinStateArgs{GetFluxPoolStateArgs: args})
	})
}

func TestSquadBinKeyedStateAgainstTwoRealBSCContractsAndPostHandle(t *testing.T) {
	for _, id := range []string{
		"0x13cf45c434e945e043df18a6e022e5fbbd24311755b9b910025fec263c1d759d",
		"0x61c3557aae1c6d0c8945baf1afce2d2af0aa7a4e16356745b9dd34020ba2f003",
	} {
		t.Run(id, func(t *testing.T) {
			read := func(c *poolCaller) ([]byte, error) {
				return readSquadBin(c, common.HexToHash(id), true)
			}
			get := func(api *ArbAPI, args GetFluxPoolStateArgs) (*GetFluxPoolStateResult, error) {
				return api.GetSquadBinState(GetSquadBinStateArgs{GetFluxPoolStateArgs: args, PoolID: id})
			}
			checkPreparedPoolStateFixture(t, "testdata/squad-bin-multipool-state.json", squadBinManager, false, id, read, get)
		})
	}
}

func checkPreparedPoolStateFixture(t *testing.T, path string, contract common.Address, bindsTime bool, poolID string, read func(*poolCaller) ([]byte, error), get func(*ArbAPI, GetFluxPoolStateArgs) (*GetFluxPoolStateResult, error)) {
	t.Helper()
	raw, err := os.ReadFile(path)
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
		Data  string `json:"data"`
		Pools []struct {
			PoolID string `json:"pool_id"`
			Data   string `json:"data"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if poolID != "" {
		for _, pool := range fixture.Pools {
			if pool.PoolID == poolID {
				fixture.Data = pool.Data
				break
			}
		}
		if fixture.Data == "" {
			t.Fatal("missing captured pool")
		}
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
	got, err := read(caller)
	if err != nil {
		t.Fatal(err)
	}
	if hexutil.Encode(got) != fixture.Data {
		t.Fatal("captured state mismatch")
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
	result, err := get(&ArbAPI{svc: svc}, args)
	if err != nil {
		t.Fatal(err)
	}
	expected := common.FromHex(fixture.Data)
	// The first two state words bind the reader to its prepared block and time.
	if bindsTime {
		copy(expected[:32], common.LeftPadBytes(new(big.Int).SetUint64(x.parent.Number.Uint64()+1).Bytes(), 32))
		copy(expected[32:64], common.LeftPadBytes(new(big.Int).SetUint64(fixture.Timestamp).Bytes(), 32))
	}
	if result.Data != hexutil.Encode(expected) || result.Boot != svc.boot || result.EnvHash == "" {
		t.Fatal("wrong post-handle state or environment")
	}
	args.ParentHash = common.Hash{}.Hex()
	if _, err := get(&ArbAPI{svc: svc}, args); err == nil {
		t.Fatal("accepted foreign parent")
	}
	if len(x.base.GetCode(contract)) != 0 {
		t.Fatal("reader changed canonical parent")
	}
}

func TestFluxPoolStateRejectsEmptyRequest(t *testing.T) {
	if _, err := (&ArbAPI{}).GetFluxPoolState(GetFluxPoolStateArgs{}); err == nil {
		t.Fatal("accepted empty request")
	}
}
