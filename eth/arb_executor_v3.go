package eth

import (
	"bytes"
	"errors"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

const executorRevisionV3 = "proxy-flash-v3"
const executorRevisionMultiAsset = "proxy-multi-asset-v5"

var errExecutorEvidence = errors.New("arb: invalid executor v3 evidence")
var ledgerTopic = crypto.Keccak256Hash([]byte("ExecutionLedger(uint256,uint256,uint256,uint256)"))
var executedTopic = crypto.Keccak256Hash([]byte("BackrunExecuted(bytes32,bytes32,uint256,uint256,uint256)"))
var executeSelectorV3 = []byte{0x75, 0x99, 0xbc, 0xfb}
var executeMultiAssetSelectorV5 = []byte{0xb8, 0x8e, 0x2b, 0x94}

func executorRevisionForData(data []byte) (string, bool) {
	if bytes.Equal(data[:minInt(len(data), 4)], executeSelectorV3) {
		return executorRevisionV3, true
	}
	if bytes.Equal(data[:minInt(len(data), 4)], executeMultiAssetSelectorV5) {
		return executorRevisionMultiAsset, true
	}
	return "", false
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Parse only our canonical ABI head. EVM execution validates the complete route.
// Enforce the signed envelope gas limit, base token and requested proxy identity.
func validateExecutorEnvelope(to *common.Address, data []byte, gas uint64, mt measureTarget) error {
	if !mt.measure || to == nil || *to != mt.executor || gas == 0 || len(data) < 4 {
		return errExecutorEvidence
	}
	revision, ok := executorRevisionForData(data)
	if !ok {
		return errExecutorEvidence
	}
	if revision == executorRevisionV3 && len(data) < 4+15*32 {
		return errExecutorEvidence
	}
	if revision == executorRevisionMultiAsset && len(data) < 4+19*32 {
		return errExecutorEvidence
	}
	word := func(i int) *big.Int { return new(big.Int).SetBytes(data[4+i*32 : 4+(i+1)*32]) }
	gasWord, financingWord := 12, 13
	if revision == executorRevisionMultiAsset {
		gasWord, financingWord = 16, 10
	}
	if word(0).Cmp(big.NewInt(32)) != 0 || word(gasWord).Cmp(new(big.Int).SetUint64(gas)) != 0 {
		return errExecutorEvidence
	}
	if revision == executorRevisionV3 && word(13).Cmp(big.NewInt(13*32)) != 0 {
		return errExecutorEvidence
	}
	if revision == executorRevisionMultiAsset && word(financingWord).Cmp(big.NewInt(18*32)) != 0 {
		return errExecutorEvidence
	}
	if revision == executorRevisionV3 && word(5).Cmp(new(big.Int).SetBytes(mt.baseToken.Bytes())) != 0 {
		return errExecutorEvidence
	}
	return nil
}

// Read only our successful transaction receipt. Both events must originate from
// the named proxy, occur once, and agree with the actual calldata audit/payment fields.
func executorLedgerV3(receipt *types.Receipt, data []byte, mt measureTarget) (map[string]any, error) {
	if receipt == nil || receipt.Status != types.ReceiptStatusSuccessful || len(data) < 4+15*32 {
		return nil, errExecutorEvidence
	}
	var ledger, executed *types.Log
	for _, l := range receipt.Logs {
		if l == nil || l.Address != mt.executor || len(l.Topics) == 0 {
			continue
		}
		if l.Topics[0] == ledgerTopic {
			if ledger != nil || l.Removed || len(l.Topics) != 1 || len(l.Data) != 128 {
				return nil, errExecutorEvidence
			}
			ledger = l
		}
		if l.Topics[0] == executedTopic {
			if executed != nil || l.Removed || len(l.Topics) != 3 || len(l.Data) != 96 {
				return nil, errExecutorEvidence
			}
			executed = l
		}
	}
	if ledger == nil || executed == nil {
		return nil, errExecutorEvidence
	}
	word := func(b []byte, i int) *big.Int { return new(big.Int).SetBytes(b[i*32 : (i+1)*32]) }
	t0, t2, t3, sweep := word(ledger.Data, 0), word(ledger.Data, 1), word(ledger.Data, 2), word(ledger.Data, 3)
	revision, ok := executorRevisionForData(data)
	if !ok {
		return nil, errExecutorEvidence
	}
	bidIndex := 10
	if revision == executorRevisionMultiAsset {
		bidIndex = 14
	}
	bid := new(big.Int).SetBytes(data[4+bidIndex*32 : 4+(bidIndex+1)*32])
	if t2.Cmp(t3) < 0 || t3.Cmp(t0) < 0 || sweep.Cmp(t3) > 0 || new(big.Int).Sub(t2, t3).Cmp(bid) != 0 {
		return nil, errExecutorEvidence
	}
	if !bytes.Equal(executed.Topics[1][:], data[4+32:4+64]) || !bytes.Equal(executed.Topics[2][:], data[4+64:4+96]) {
		return nil, errExecutorEvidence
	}
	if word(executed.Data, 0).Cmp(new(big.Int).Sub(t2, t0)) != 0 || word(executed.Data, 1).Cmp(bid) != 0 || word(executed.Data, 2).Cmp(new(big.Int).Sub(t3, t0)) != 0 {
		return nil, errExecutorEvidence
	}
	return map[string]any{"executor_revision": revision, "measurement": "executor_event_pre_sweep", "executor": strings.ToLower(mt.executor.Hex()), "base_token": strings.ToLower(mt.baseToken.Hex()), "retained_t0": t0.String(), "retained_t2": t2.String(), "retained_t3": t3.String(), "builder_payment": bid.String(), "owner_sweep": sweep.String()}, nil
}

func blockEnvDigestV3(e arb.BlockEnv) ([32]byte, error) {
	var zero [32]byte
	if err := arb.ValidateBlockEnv(e); err != nil {
		return zero, err
	}
	dec := func(s string, n int) ([]byte, error) {
		if s == "" || (len(s) > 1 && s[0] == '0') {
			return nil, errExecutorEvidence
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return nil, errExecutorEvidence
			}
		}
		v, ok := new(big.Int).SetString(s, 10)
		if !ok || v.BitLen() > n*8 {
			return nil, errExecutorEvidence
		}
		b := make([]byte, n)
		v.FillBytes(b)
		return b, nil
	}
	d := arb.NewDigest("arb.env.v1")
	fields := []struct {
		s        string
		n        int
		hex      bool
		optional bool
		present  bool
	}{
		{e.Number, 8, false, false, true}, {e.ParentHash, 32, true, false, true}, {e.TimestampMs, 8, false, false, true}, {e.TimestampSeconds, 8, false, false, true}, {e.Author, 20, true, false, true}, {e.GasLimit, 8, false, false, true},
		{ptrValue(e.BaseFee), 32, false, true, e.BaseFee != nil}, {e.Difficulty, 32, false, false, true}, {e.MixDigest, 32, true, false, true},
		{ptrValue(e.BlobGasUsed), 8, false, true, e.BlobGasUsed != nil}, {ptrValue(e.ExcessBlobGas), 8, false, true, e.ExcessBlobGas != nil}, {ptrValue(e.ParentBeaconRoot), 32, true, true, e.ParentBeaconRoot != nil},
		{e.ExtraData, 0, true, false, true}, {e.ForkRulesDigest, 32, true, false, true},
	}
	for _, f := range fields {
		if !f.present {
			d.FieldBytes([]byte{0})
			continue
		}
		var b []byte
		var err error
		if f.hex {
			b, err = hexutil.Decode(f.s)
			if f.n != 0 && len(b) != f.n {
				return zero, errExecutorEvidence
			}
		} else {
			b, err = dec(f.s, f.n)
		}
		if err != nil {
			return zero, err
		}
		if f.optional {
			b = append([]byte{1}, b...)
		}
		d.FieldBytes(b)
	}
	return d.Finalize(), nil
}
func ptrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func signedRequestDigestV3(parent, stamp string, env, raw [32]byte, mt measureTarget) [32]byte {
	return signedRequestDigestWithRevision(parent, stamp, env, raw, mt, executorRevisionV3)
}
func signedRequestDigestWithRevision(parent, stamp string, env, raw [32]byte, mt measureTarget, revision string) [32]byte {
	return arb.NewDigest("arb.req.signedBundle.v3").FieldBytes(common.FromHex(parent)).FieldBytes(common.FromHex(stamp)).FieldBytes(env[:]).FieldBytes(raw[:]).FieldBytes(mt.executor.Bytes()).FieldBytes(mt.baseToken.Bytes()).FieldBytes([]byte(revision)).Finalize()
}
func bundleDigestV3(raws []string) ([32]byte, error) {
	d := arb.NewDigest("arb.bundle.v1")
	// raw_digest folds the ordered raw transaction bytes, prefixed by count.
	d.FieldU32(uint32(len(raws)))
	for _, raw := range raws {
		b, e := hexutil.Decode(raw)
		if e != nil {
			return [32]byte{}, e
		}
		d.FieldBytes(b)
	}
	return d.Finalize(), nil
}
