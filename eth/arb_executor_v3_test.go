package eth

import (
	_ "embed"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/arb"
)

//go:embed arb/testdata/executor-v3.json
var executorVectorJSON []byte

func TestExecutorV3SharedDigests(t *testing.T) {
	var v struct {
		Signed map[string]json.RawMessage `json:"signed_bundle_digest"`
		Envs   []struct {
			Env      arb.BlockEnv `json:"env"`
			Expected string       `json:"expected"`
		} `json:"block_env_digest"`
	}
	if err := json.Unmarshal(executorVectorJSON, &v); err != nil {
		t.Fatal(err)
	}
	get := func(k string) string {
		var s string
		if err := json.Unmarshal(v.Signed[k], &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	var raws []string
	json.Unmarshal(v.Signed["raws"], &raws)
	raw, err := bundleDigestV3(raws)
	if err != nil || hex32Str(raw) != get("raw_digest") {
		t.Fatalf("raw digest %x %v", raw, err)
	}
	env := common.HexToHash(get("block_env_hash"))
	mt, _ := decodeMeasureTarget(get("executor"), get("base_token"))
	req := signedRequestDigestV3(get("parent_handle"), get("stamp"), env, raw, mt)
	if hex32Str(req) != get("request_digest") {
		t.Fatalf("request digest %x", req)
	}
	swapped, _ := bundleDigestV3([]string{raws[1], raws[0]})
	if swapped == raw {
		t.Fatal("order lost")
	}
	for _, vec := range v.Envs {
		got, err := blockEnvDigestV3(vec.Env)
		if err != nil || hex32Str(got) != vec.Expected {
			t.Fatalf("env mismatch %x %s %v", got, vec.Expected, err)
		}
		vec.Env.Difficulty = "115792089237316195423570985008687907853269984665640564039457584007913129639936"
		if _, err := blockEnvDigestV3(vec.Env); err == nil {
			t.Fatal("uint256 overflow accepted")
		}
	}
}

func TestExecutorV3ReceiptAndEnvelope(t *testing.T) {
	var v struct {
		Calldata []struct {
			Expected string `json:"expected"`
		} `json:"calldata"`
	}
	if err := json.Unmarshal(executorVectorJSON, &v); err != nil {
		t.Fatal(err)
	}
	data := common.FromHex(v.Calldata[0].Expected)
	mt := measureTarget{measure: true, executor: common.HexToAddress("0x1234"), baseToken: common.HexToAddress("0xbb")}
	if err := validateExecutorEnvelope(&mt.executor, data, 1400000, mt); err != nil {
		t.Fatal(err)
	}
	if err := validateExecutorEnvelope(&mt.executor, data, 1400001, mt); err == nil {
		t.Fatal("gas mismatch accepted")
	}
	amounts := func(ns ...int64) []byte {
		var b []byte
		for _, n := range ns {
			b = append(b, common.LeftPadBytes(big.NewInt(n).Bytes(), 32)...)
		}
		return b
	}
	makeReceipt := func() *types.Receipt {
		return &types.Receipt{Status: 1, Logs: []*types.Log{
			{Address: mt.executor, Topics: []common.Hash{executedTopic, common.BytesToHash(data[36:68]), common.BytesToHash(data[68:100])}, Data: amounts(8000, 5000, 3000)},
			{Address: mt.executor, Topics: []common.Hash{ledgerTopic}, Data: amounts(2000, 10000, 5000, 5000)},
		}}
	}
	p, err := executorLedgerV3(makeReceipt(), data, mt)
	if err != nil || p["retained_t3"] != "5000" {
		t.Fatalf("ledger %v %v", p, err)
	}
	for _, corrupt := range []func(*types.Receipt){
		func(r *types.Receipt) { r.Status = 0 },
		func(r *types.Receipt) { r.Logs[1].Address = common.Address{} },
		func(r *types.Receipt) { r.Logs = append(r.Logs, r.Logs[1]) },
		func(r *types.Receipt) { r.Logs[1].Data = amounts(2000, 10000, 5001, 5000) },
		func(r *types.Receipt) { r.Logs[0].Topics[1] = common.Hash{} },
		func(r *types.Receipt) { r.Logs[1].Removed = true },
	} {
		r := makeReceipt()
		corrupt(r)
		if _, err := executorLedgerV3(r, data, mt); err == nil {
			t.Fatal("corrupt receipt accepted")
		}
	}
}

func TestExecutorV5MultiAssetEnvelopeAndLedgerPaymentWord(t *testing.T) {
	data := make([]byte, 4+19*32)
	copy(data[:4], executeMultiAssetSelectorV5)
	word := func(i int, n int64) { big.NewInt(n).FillBytes(data[4+i*32:4+(i+1)*32]) }
	word(0, 32) // outer tuple offset
	word(10, 18*32) // financingAdapterData offset relative to tuple
	word(16, 300000) // gasLimit
	word(13, 12345) // builder recipient must not be interpreted as payment
	word(14, 7) // builder payment including the outer tuple offset
	mt := measureTarget{measure: true, executor: common.HexToAddress("0x1234"), baseToken: common.HexToAddress("0xbb")}
	if err := validateExecutorEnvelope(&mt.executor, data, 300000, mt); err != nil { t.Fatal(err) }
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{
		{Address: mt.executor, Topics: []common.Hash{executedTopic, common.BytesToHash(data[36:68]), common.BytesToHash(data[68:100])}, Data: common.LeftPadBytes(big.NewInt(8000).Bytes(), 96)},
		{Address: mt.executor, Topics: []common.Hash{ledgerTopic}, Data: common.LeftPadBytes(big.NewInt(2000).Bytes(), 128)},
	}}
	// Build four independent 32-byte ledger words for the accounting equation.
	receipt.Logs[0].Data = append(append(common.LeftPadBytes(big.NewInt(6000).Bytes(), 32), common.LeftPadBytes(big.NewInt(7).Bytes(), 32)...), common.LeftPadBytes(big.NewInt(5993).Bytes(), 32)...)
	receipt.Logs[1].Data = append(append(append(common.LeftPadBytes(big.NewInt(2000).Bytes(), 32), common.LeftPadBytes(big.NewInt(8000).Bytes(), 32)...), common.LeftPadBytes(big.NewInt(7993).Bytes(), 32)...), common.LeftPadBytes(big.NewInt(7993).Bytes(), 32)...)
	p, err := executorLedgerV3(receipt, data, mt)
	if err != nil || p["executor_revision"] != executorRevisionMultiAsset { t.Fatalf("multi asset ledger %v %v", p, err) }
}
