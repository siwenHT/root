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

// Current multi-asset revision: gas-first settlement (V4) behind the same proxy.
const executorRevisionMultiAsset = "proxy-multi-asset-v6"

// Superseded fixed-split multi-asset revision, still decoded on old receipts.
const executorRevisionMultiAssetV5 = "proxy-multi-asset-v5"
const executorRevisionLunarDirect = "proxy-lunar-direct-v1"
const executorRevisionLunarForward = "proxy-lunar-forward-v1"
const executorRevisionTenxDirect = "proxy-tenx-direct-v1"

func knownExecutorRevision(r string) bool {
	return r == executorRevisionV3 || r == executorRevisionMultiAsset || r == executorRevisionMultiAssetV5 || r == executorRevisionLunarDirect || r == executorRevisionLunarForward || r == executorRevisionTenxDirect
}

var errExecutorEvidence = errors.New("arb: invalid executor v3 evidence")
var ledgerTopic = crypto.Keccak256Hash([]byte("ExecutionLedger(uint256,uint256,uint256,uint256)"))
var executedTopic = crypto.Keccak256Hash([]byte("BackrunExecuted(bytes32,bytes32,uint256,uint256,uint256)"))

// Canonical dispatch heads of our own executor. The `...Share` selectors are the
// dynamic builder-share ABI now deployed on chain; the `...Fixed` selectors are
// the superseded fixed-payment ABI, kept so pre-upgrade receipts still decode.
var (
	executeSelectorV3Fixed      = []byte{0x75, 0x99, 0xbc, 0xfb}
	executeSelectorV3Share      = []byte{0xbc, 0x20, 0xa7, 0x70}
	executeMultiSelectorFixed   = []byte{0xb8, 0x8e, 0x2b, 0x94}
	executeMultiSelectorShare   = []byte{0x95, 0xbc, 0xf8, 0xd2}
	executeLunarDirectSelector  = []byte{0x17, 0x05, 0x99, 0x57}
	executeLunarForwardSelector = []byte{0x2a, 0xb5, 0xe6, 0xb6}
	executeTenxDirectSelector   = []byte{0x34, 0xce, 0xa7, 0xc3}
	directWBNB                  = common.HexToAddress("0xbb4cdb9cbd36b01bd1cbaebf2de08d9173bc095c")
)

// executeLayout pins the canonical ABI head we accept. Indexes address the
// argument area as data[4+i*32:], so word 0 is the outer dynamic tuple offset.
// `shareWord` holds builderShareBps (dynamic payment) and `paymentWord` holds a
// fixed wei payment; exactly one of the two is set.
type executeLayout struct {
	minWords      int
	gasWord       int
	offsetWord    int
	offsetValue   int64
	baseTokenWord int
	shareWord     int
	paymentWord   int
	staticTuple   bool
}

func executeLayoutFor(data []byte) (executeLayout, string, bool) {
	if len(data) < 4 {
		return executeLayout{}, "", false
	}
	switch {
	case bytes.Equal(data[:4], executeSelectorV3Fixed):
		return executeLayout{minWords: 15, gasWord: 12, offsetWord: 13, offsetValue: 13 * 32, baseTokenWord: 5, shareWord: -1, paymentWord: 10}, executorRevisionV3, true
	case bytes.Equal(data[:4], executeSelectorV3Share):
		return executeLayout{minWords: 13, gasWord: 11, offsetWord: 12, offsetValue: 12 * 32, baseTokenWord: 5, shareWord: 9, paymentWord: -1}, executorRevisionV3, true
	case bytes.Equal(data[:4], executeMultiSelectorFixed):
		return executeLayout{minWords: 19, gasWord: 16, offsetWord: 10, offsetValue: 18 * 32, baseTokenWord: -1, shareWord: -1, paymentWord: 14}, executorRevisionMultiAssetV5, true
	case bytes.Equal(data[:4], executeMultiSelectorShare):
		return executeLayout{minWords: 17, gasWord: 15, offsetWord: 10, offsetValue: 17 * 32, baseTokenWord: -1, shareWord: 13, paymentWord: -1}, executorRevisionMultiAsset, true
	case bytes.Equal(data[:4], executeLunarDirectSelector):
		return executeLayout{minWords: 10, gasWord: 9, offsetWord: -1, baseTokenWord: -1, shareWord: 7, paymentWord: -1, staticTuple: true}, executorRevisionLunarDirect, true
	case bytes.Equal(data[:4], executeLunarForwardSelector):
		return executeLayout{minWords: 10, gasWord: 9, offsetWord: -1, baseTokenWord: -1, shareWord: 7, paymentWord: -1, staticTuple: true}, executorRevisionLunarForward, true
	case bytes.Equal(data[:4], executeTenxDirectSelector):
		return executeLayout{minWords: 13, gasWord: 9, offsetWord: -1, baseTokenWord: -1, shareWord: 7, paymentWord: -1, staticTuple: true}, executorRevisionTenxDirect, true
	}
	return executeLayout{}, "", false
}

func executorRevisionForData(data []byte) (string, bool) {
	_, revision, ok := executeLayoutFor(data)
	return revision, ok
}

// Parse only our canonical ABI head. EVM execution validates the complete route.
// Enforce the signed envelope gas limit, base token and requested proxy identity.
func validateExecutorEnvelope(to *common.Address, data []byte, gas uint64, mt measureTarget) error {
	if !mt.measure || to == nil || *to != mt.executor || gas == 0 || len(data) < 4 {
		return errExecutorEvidence
	}
	layout, _, ok := executeLayoutFor(data)
	if !ok || len(data) < 4+layout.minWords*32 {
		return errExecutorEvidence
	}
	word := func(i int) *big.Int { return new(big.Int).SetBytes(data[4+i*32 : 4+(i+1)*32]) }
	if word(layout.gasWord).Cmp(new(big.Int).SetUint64(gas)) != 0 {
		return errExecutorEvidence
	}
	if layout.staticTuple {
		if len(data) != 4+layout.minWords*32 || mt.baseToken != directWBNB {
			return errExecutorEvidence
		}
	} else if word(0).Cmp(big.NewInt(32)) != 0 || word(layout.offsetWord).Cmp(big.NewInt(layout.offsetValue)) != 0 {
		return errExecutorEvidence
	}
	if layout.baseTokenWord >= 0 && word(layout.baseTokenWord).Cmp(new(big.Int).SetBytes(mt.baseToken.Bytes())) != 0 {
		return errExecutorEvidence
	}
	if layout.shareWord >= 0 && word(layout.shareWord).Cmp(big.NewInt(10_000)) > 0 {
		return errExecutorEvidence
	}
	return nil
}

// Read only our successful transaction receipt. Both events must originate from
// the named proxy, occur once, and agree with the actual calldata audit/payment fields.
func executorLedgerV3(receipt *types.Receipt, data []byte, mt measureTarget) (map[string]any, error) {
	if receipt == nil || receipt.Status != types.ReceiptStatusSuccessful {
		return nil, errExecutorEvidence
	}
	layout, revision, ok := executeLayoutFor(data)
	if !ok || len(data) < 4+layout.minWords*32 {
		return nil, errExecutorEvidence
	}
	if layout.staticTuple && (len(data) != 4+layout.minWords*32 || mt.baseToken != directWBNB) {
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
	dataWord := func(i int) *big.Int { return new(big.Int).SetBytes(data[4+i*32 : 4+(i+1)*32]) }
	t0, t2, t3, sweep := word(ledger.Data, 0), word(ledger.Data, 1), word(ledger.Data, 2), word(ledger.Data, 3)
	if t2.Cmp(t3) < 0 || t3.Cmp(t0) < 0 || sweep.Cmp(t3) > 0 {
		return nil, errExecutorEvidence
	}
	gross := new(big.Int).Sub(t2, t0)
	payment := new(big.Int).Sub(t2, t3)
	if layout.shareWord >= 0 {
		share := dataWord(layout.shareWord)
		if share.Cmp(big.NewInt(10_000)) > 0 {
			return nil, errExecutorEvidence
		}
		// Dynamic-share ABI. The fixed-split executor pays gross*share; the
		// gas-first executor reserves gas first and pays less. Accept [0, gross*share].
		maxPayment := new(big.Int).Mul(gross, share)
		maxPayment.Div(maxPayment, big.NewInt(10_000))
		if payment.Sign() < 0 || payment.Cmp(maxPayment) > 0 {
			return nil, errExecutorEvidence
		}
	} else if payment.Cmp(dataWord(layout.paymentWord)) != 0 {
		return nil, errExecutorEvidence
	}
	idWord := 1
	if layout.staticTuple {
		idWord = 0
	}
	if !bytes.Equal(executed.Topics[1][:], data[4+idWord*32:4+(idWord+1)*32]) || !bytes.Equal(executed.Topics[2][:], data[4+(idWord+1)*32:4+(idWord+2)*32]) {
		return nil, errExecutorEvidence
	}
	if word(executed.Data, 0).Cmp(gross) != 0 || word(executed.Data, 1).Cmp(payment) != 0 || word(executed.Data, 2).Cmp(new(big.Int).Sub(t3, t0)) != 0 {
		return nil, errExecutorEvidence
	}
	return map[string]any{"executor_revision": revision, "measurement": "executor_event_pre_sweep", "executor": strings.ToLower(mt.executor.Hex()), "base_token": strings.ToLower(mt.baseToken.Hex()), "retained_t0": t0.String(), "retained_t2": t2.String(), "retained_t3": t3.String(), "builder_payment": payment.String(), "owner_sweep": sweep.String()}, nil
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
