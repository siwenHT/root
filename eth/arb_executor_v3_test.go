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

func TestExecutorV5MultiAssetEnvelopeAndLedgerSharePayment(t *testing.T) {
	data := make([]byte, 4+17*32)
	copy(data[:4], executeMultiSelectorShare)
	word := func(i int, n int64) { big.NewInt(n).FillBytes(data[4+i*32 : 4+(i+1)*32]) }
	word(0, 32)      // outer tuple offset
	word(10, 17*32)  // financingAdapterData offset relative to tuple
	word(15, 300000) // gasLimit
	word(12, 12345)  // builder recipient must not be interpreted as payment
	word(13, 7000)   // builderShareBps: payment is gross*share/10000, not a fixed wei value
	mt := measureTarget{measure: true, executor: common.HexToAddress("0x1234"), baseToken: common.HexToAddress("0xbb")}
	if err := validateExecutorEnvelope(&mt.executor, data, 300000, mt); err != nil {
		t.Fatal(err)
	}
	// gross 6000, share 7000 bps -> payment 4200, retained 1800.
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{
		{Address: mt.executor, Topics: []common.Hash{executedTopic, common.BytesToHash(data[36:68]), common.BytesToHash(data[68:100])}, Data: words(6000, 4200, 1800)},
		{Address: mt.executor, Topics: []common.Hash{ledgerTopic}, Data: words(2000, 8000, 3800, 3800)},
	}}
	p, err := executorLedgerV3(receipt, data, mt)
	if err != nil || p["executor_revision"] != executorRevisionMultiAsset {
		t.Fatalf("multi asset ledger %v %v", p, err)
	}
	if p["builder_payment"] != "4200" || p["retained_t3"] != "3800" {
		t.Fatalf("share payment mismatch %v", p)
	}
	// A payment word above the real share-derived amount must be rejected.
	bad := &types.Receipt{Status: 1, Logs: []*types.Log{
		{Address: mt.executor, Topics: []common.Hash{executedTopic, common.BytesToHash(data[36:68]), common.BytesToHash(data[68:100])}, Data: words(6000, 4201, 1800)},
		{Address: mt.executor, Topics: []common.Hash{ledgerTopic}, Data: words(2000, 8000, 3800, 3800)},
	}}
	if _, err := executorLedgerV3(bad, data, mt); err == nil {
		t.Fatal("over-reported builder payment accepted")
	}
	// Share above 100% is rejected before any receipt work.
	word(13, 10001)
	if err := validateExecutorEnvelope(&mt.executor, data, 300000, mt); err == nil {
		t.Fatal("builder share above 100% accepted")
	}
}

func TestExecutorV5LegacyShareEnvelopeAndLedger(t *testing.T) {
	data := make([]byte, 4+13*32)
	copy(data[:4], executeSelectorV3Share)
	base := common.HexToAddress("0xbb")
	word := func(i int, n int64) { big.NewInt(n).FillBytes(data[4+i*32 : 4+(i+1)*32]) }
	word(0, 32)     // outer tuple offset
	word(5, 0xbb)   // baseToken word
	word(9, 6000)   // builderShareBps
	word(11, 1400000)
	word(12, 12*32) // legs offset after a 12-word tuple head
	mt := measureTarget{measure: true, executor: common.HexToAddress("0x1234"), baseToken: base}
	if err := validateExecutorEnvelope(&mt.executor, data, 1400000, mt); err != nil {
		t.Fatal(err)
	}
	if err := validateExecutorEnvelope(&mt.executor, data, 1400001, mt); err == nil {
		t.Fatal("gas mismatch accepted")
	}
	// gross 10000, share 6000 bps -> payment 6000, retained 4000.
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{
		{Address: mt.executor, Topics: []common.Hash{executedTopic, common.BytesToHash(data[36:68]), common.BytesToHash(data[68:100])}, Data: words(10000, 6000, 4000)},
		{Address: mt.executor, Topics: []common.Hash{ledgerTopic}, Data: words(1000, 11000, 5000, 5000)},
	}}
	p, err := executorLedgerV3(receipt, data, mt)
	if err != nil || p["executor_revision"] != executorRevisionV3 || p["builder_payment"] != "6000" {
		t.Fatalf("legacy share ledger %v %v", p, err)
	}
}

func TestExecutorLunarDirectStaticEnvelopeAndLedger(t *testing.T) {
	data := make([]byte, 4+10*32)
	copy(data[:4], executeLunarDirectSelector)
	word := func(i int, n int64) { big.NewInt(n).FillBytes(data[4+i*32 : 4+(i+1)*32]) }
	word(0, 1) // opportunityId; there is no outer dynamic tuple offset
	word(1, 2) // targetTxHash
	word(4, 1_000_000)
	word(5, 50_000_000)
	word(7, 6000) // builderShareBps
	word(9, 1_500_000)
	mt := measureTarget{measure: true, executor: common.HexToAddress("0x1234"), baseToken: directWBNB}
	if !knownExecutorRevision(executorRevisionLunarDirect) {
		t.Fatal("direct revision unknown")
	}
	if revision, ok := executorRevisionForData(data); !ok || revision != executorRevisionLunarDirect {
		t.Fatal("direct selector revision mismatch")
	}
	if err := validateExecutorEnvelope(&mt.executor, data, 1_500_000, mt); err != nil {
		t.Fatal(err)
	}
	receipt := &types.Receipt{Status: 1, Logs: []*types.Log{
		{Address: mt.executor, Topics: []common.Hash{executedTopic, common.BytesToHash(data[4:36]), common.BytesToHash(data[36:68])}, Data: words(10000, 4000, 6000)},
		{Address: mt.executor, Topics: []common.Hash{ledgerTopic}, Data: words(1000, 11000, 7000, 6000)},
	}}
	ledger, err := executorLedgerV3(receipt, data, mt)
	if err != nil || ledger["executor_revision"] != executorRevisionLunarDirect {
		t.Fatalf("direct ledger %v %v", ledger, err)
	}
	for _, invalid := range [][]byte{data[:len(data)-1], append(append([]byte{}, data...), 0)} {
		if err := validateExecutorEnvelope(&mt.executor, invalid, 1_500_000, mt); err == nil {
			t.Fatal("noncanonical static tuple accepted")
		}
	}
	if err := validateExecutorEnvelope(&mt.executor, data, 1_500_001, mt); err == nil {
		t.Fatal("gas mismatch accepted")
	}
	wrongBase := mt
	wrongBase.baseToken = common.HexToAddress("0x1235")
	if err := validateExecutorEnvelope(&mt.executor, data, 1_500_000, wrongBase); err == nil {
		t.Fatal("wrong settlement token accepted")
	}
	word(7, 10_001)
	if err := validateExecutorEnvelope(&mt.executor, data, 1_500_000, mt); err == nil {
		t.Fatal("share above 100% accepted")
	}
}

func words(ns ...int64) []byte {
	var b []byte
	for _, n := range ns {
		b = append(b, common.LeftPadBytes(big.NewInt(n).Bytes(), 32)...)
	}
	return b
}
