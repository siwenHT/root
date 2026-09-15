package eth

import (
	"bytes"
	"math/big"
	"strconv"
	"strings"
	"testing"

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

// Execute a real signed call whose log records COINBASE, TIMESTAMP, NUMBER,
// DIFFICULTY, GASLIMIT, CHAINID, BASEFEE and BLOCKHASH(parent). Compare against
// core.GenerateChain's native transaction processing on the same parent state.
func TestEnvV3OpcodeNativeParity(t *testing.T) {
	key, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	sender := crypto.PubkeyToAddress(key.PublicKey)
	contract := common.HexToAddress("0x1001")
	author := common.HexToAddress("0x1002")
	code := []byte{}
	for i, op := range []byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x48} {
		code = append(code, op, 0x60, byte(i*32), 0x52)
	}
	code = append(code, 0x60, 0x01, 0x43, 0x03, 0x40, 0x60, 0xe0, 0x52, 0x61, 0x01, 0x00, 0x60, 0x00, 0xa0, 0x00)
	gdb, db := rawdb.NewMemoryDatabase(), rawdb.NewMemoryDatabase()
	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{
		sender:   {Balance: new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)},
		contract: {Code: code, Balance: new(big.Int)},
	}}
	gb := genesis.MustCommit(gdb, triedb.NewDatabase(gdb, triedb.HashDefaults))
	chain, receipts := core.GenerateChain(genesis.Config, gb, ethash.NewFaker(), gdb, 2, func(i int, g *core.BlockGen) {
		g.SetCoinbase(author)
		tx, err := types.SignTx(types.NewTransaction(uint64(i), contract, new(big.Int), 150000, g.BaseFee(), nil), types.LatestSigner(genesis.Config), key)
		if err != nil {
			panic(err)
		}
		g.AddTx(tx)
	})
	bc, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Stop()
	if _, err = bc.InsertChain(chain); err != nil {
		t.Fatal(err)
	}
	parent := gb.Header()
	for i, b := range chain {
		base, err := bc.StateAt(parent.Root)
		if err != nil {
			t.Fatal(err)
		}
		x := newTargetExecutor(bc, parent, base)
		h := b.Header()
		env := arb.BlockEnv{Number: h.Number.String(), ParentHash: h.ParentHash.Hex(), TimestampSeconds: strconv.FormatUint(h.Time, 10), TimestampMs: strconv.FormatUint(h.Time*1000, 10), Author: strings.ToLower(author.Hex()), GasLimit: strconv.FormatUint(h.GasLimit, 10), Difficulty: h.Difficulty.String(), MixDigest: h.MixDigest.Hex(), ExtraData: "0x" + common.Bytes2Hex(h.Extra), ForkRulesDigest: common.Hash{}.Hex()}
		env, err = x.prepareBlockEnv(env)
		if err != nil {
			t.Fatal(err)
		}
		// Decode the exact signed raw, including sender recovery in ExecuteTarget.
		raw, _ := b.Transactions()[0].MarshalBinary()
		tx := new(types.Transaction)
		if err = tx.UnmarshalBinary(raw); err != nil {
			t.Fatal(err)
		}
		got, err := x.ExecuteTargetWithEnv(tx, newReadBudget(1000000, 0), env)
		if err != nil {
			t.Fatal(err)
		}
		want := receipts[i][0]
		if !got.Class.ReceiptTrusted || got.Receipt.Status != want.Status || got.UsedGas != want.GasUsed || len(got.Receipt.Logs) != 1 || !bytes.Equal(got.Receipt.Logs[0].Data, want.Logs[0].Data) {
			t.Fatalf("native env parity failed at %d: %+v", i, got)
		}
		log := got.Receipt.Logs[0].Data
		if common.BytesToHash(log[224:256]) != parent.Hash() {
			t.Fatal("BLOCKHASH must resolve immediate parent")
		}
		parent = h
	}
}

type envConfigChain struct {
	core.ChainContext
	cfg *params.ChainConfig
}

func (c envConfigChain) Config() *params.ChainConfig { return c.cfg }

func TestEnvV3StrictDerivedFields(t *testing.T) {
	x, _, bc := prefixFixture(t)
	defer bc.Stop()
	cfg := *params.BSCChainConfig
	x.chain = envConfigChain{x.chain, &cfg}
	x.parent = types.CopyHeader(x.parent)
	x.parent.Number = big.NewInt(500000000)
	x.parent.Time = 1900000000
	x.parent.Difficulty = big.NewInt(2)
	x.parent.BaseFee = new(big.Int)
	zero := uint64(0)
	x.parent.BlobGasUsed = &zero
	x.parent.ExcessBlobGas = &zero
	env := validEnvForExecutor(x)
	if env.BaseFee == nil || env.ExcessBlobGas == nil || env.ParentBeaconRoot == nil {
		t.Fatal("active BSC derived fields omitted")
	}
	if _, err := x.resolveBlockEnv(env); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*arb.BlockEnv){
		"fork digest":           func(e *arb.BlockEnv) { e.ForkRulesDigest = common.Hash{}.Hex() },
		"base omitted":          func(e *arb.BlockEnv) { e.BaseFee = nil },
		"excess omitted":        func(e *arb.BlockEnv) { e.ExcessBlobGas = nil },
		"beacon omitted":        func(e *arb.BlockEnv) { e.ParentBeaconRoot = nil },
		"beacon wrong":          func(e *arb.BlockEnv) { v := common.HexToHash("0x1234").Hex(); e.ParentBeaconRoot = &v },
		"mix milliseconds":      func(e *arb.BlockEnv) { e.MixDigest = common.HexToHash("0x01").Hex() },
		"oversized mix":         func(e *arb.BlockEnv) { e.MixDigest = common.HexToHash("0x10000000000000000").Hex() },
		"blob invalid multiple": func(e *arb.BlockEnv) { v := "1"; e.BlobGasUsed = &v },
		"gas bound":             func(e *arb.BlockEnv) { e.GasLimit = "9223372036854775808" },
		"fake difficulty":       func(e *arb.BlockEnv) { e.Difficulty = "0" },
		"overflow":              func(e *arb.BlockEnv) { e.Number = "18446744073709551616" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := env
			mutate(&e)
			if _, err := x.resolveBlockEnv(e); err == nil {
				t.Fatal("accepted invalid env")
			}
		})
	}
	// A fractional timestamp must be encoded into the actual synthetic header.
	env.TimestampMs = strconv.FormatUint((x.parent.Time+1)*1000+250, 10)
	env.MixDigest = common.HexToHash("0xfa").Hex()
	env, err := x.prepareBlockEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := x.resolveBlockEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.header.MilliTimestamp() != (x.parent.Time+1)*1000+250 {
		t.Fatal("lost milliseconds")
	}
}

func TestEnvV3BeaconPreambleAndRandom(t *testing.T) {
	x, _, bc := prefixFixture(t)
	defer bc.Stop()
	cfg := *params.TestChainConfig
	zero := uint64(0)
	cfg.ShanghaiTime = &zero
	cfg.CancunTime = &zero
	cfg.BlobScheduleConfig = params.DefaultBlobSchedule
	x.chain = envConfigChain{x.chain, &cfg}
	// A small stateful beacon-root receiver makes the preamble observable.
	x.base.SetCode(params.BeaconRootsAddress, []byte{0x60, 0x00, 0x35, 0x60, 0x00, 0x55, 0x00}, 0)
	env := arb.BlockEnv{Number: new(big.Int).Add(x.parent.Number, big.NewInt(1)).String(), ParentHash: x.parent.Hash().Hex(), TimestampSeconds: strconv.FormatUint(x.parent.Time+10, 10), TimestampMs: strconv.FormatUint((x.parent.Time+10)*1000, 10), Author: strings.ToLower(common.HexToAddress("0x1002").Hex()), GasLimit: strconv.FormatUint(x.parent.GasLimit, 10), Difficulty: "0", MixDigest: common.HexToHash("0x123456").Hex(), ExtraData: "0x1234", ForkRulesDigest: common.Hash{}.Hex()}
	root := common.HexToHash("0xabcdef")
	rootHex := root.Hex()
	env.ParentBeaconRoot = &rootHex
	env, err := x.prepareBlockEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := x.resolveBlockEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	session := x.newExecSession(newReadBudget(1000000, 0), resolved)
	if session.evm.Context.Random == nil || *session.evm.Context.Random != common.HexToHash(env.MixDigest) {
		t.Fatal("PREVRANDAO not wired")
	}
	if session.evm.Context.BlobBaseFee == nil || session.evm.Context.BlobBaseFee.Sign() <= 0 {
		t.Fatal("BLOBBASEFEE not wired")
	}
	if session.work.GetState(params.BeaconRootsAddress, common.Hash{}) != root {
		t.Fatal("beacon preamble did not run")
	}
	if session.header.Time != x.parent.Time+10 || !bytes.Equal(session.header.Extra, []byte{0x12, 0x34}) {
		t.Fatal("header environment lost")
	}
}
