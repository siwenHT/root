// Block-env adapter (NODE shared infra, design §7.1). It turns a wire arb.BlockEnv —
// already shape/consistency-validated by arb.ValidateBlockEnv — into the CONCRETE geth
// values the executor needs, and performs the node-dependent checks that the pure core
// deliberately deferred (file header of eth/arb/block_env.go):
//
//   - parent LINKAGE: env.parent_hash must equal the bound parent's hash, and
//     env.number must be exactly parent.Number+1 (this is an N+1 simulation).
//   - base_fee: RECOMPUTED from parent+fork rules via eip1559.CalcBaseFee and, when the
//     env carries one, checked to MATCH — a client-supplied base_fee is never trusted
//     as an arbitrary value, it is verified against the node's own derivation (§7.1).
//
// Honesty: author is taken from the env verbatim (already proven non-zero by the pure
// validator); we never silently substitute a zero. Timestamps use the env's ms/seconds
// (already proven consistent). Nothing here fabricates a deferred check.
package eth

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/arb"
	"github.com/ethereum/go-ethereum/params"
)

// resolvedBlockEnv is the concrete, node-verified target block environment. Every
// field is ready to drop into a vm.BlockContext / message build; no wire strings.
type resolvedBlockEnv struct {
	header     *types.Header
	number     *big.Int
	timeSec    uint64
	timeMs     uint64
	author     common.Address
	gasLimit   uint64
	difficulty *big.Int
	baseFee    *big.Int // node-derived (verified against env when env supplied one)
}

// Errors surfaced by env resolution (adapter-side, node-dependent). Wire-shape errors
// still come from arb.ValidateBlockEnv (errors.Is ErrBlockEnvBadField etc.).
var (
	ErrEnvParentHashMismatch = errors.New("arb: block_env parent_hash does not match bound parent")
	ErrEnvNotParentPlusOne   = errors.New("arb: block_env number must be parent.Number+1 (N+1 simulation)")
	ErrEnvBaseFeeMismatch    = errors.New("arb: block_env base_fee does not match node-derived base fee")
	ErrEnvRulesMismatch      = errors.New("arb: block_env fork rules mismatch")
	ErrEnvDerivedMismatch    = errors.New("arb: block_env derived fields missing or inconsistent")
	ErrEnvParseField         = errors.New("arb: block_env field failed concrete parse")
)

// resolveBlockEnv validates + links + parses a wire env against the given parent. On
// success the returned resolvedBlockEnv is authoritative for building the target block
// context. It is the ONE place the deferred node-dependent checks live.
func (x *targetExecutor) resolveBlockEnv(env arb.BlockEnv) (*resolvedBlockEnv, error) {
	// 1) Pure wire-shape + node-independent consistency (author non-zero, ts, etc.).
	if err := arb.ValidateBlockEnv(env); err != nil {
		return nil, err
	}

	// 2) Concrete parses. isUintStr already proved these are clean decimal uints, so a
	//    SetString failure would be an internal contradiction; we still check honestly.
	number, ok := new(big.Int).SetString(env.Number, 10)
	if !ok {
		return nil, fmt.Errorf("%w: number", ErrEnvParseField)
	}
	difficulty, ok := new(big.Int).SetString(env.Difficulty, 10)
	if !ok {
		return nil, fmt.Errorf("%w: difficulty", ErrEnvParseField)
	}
	gasLimit, err := parseUint64(env.GasLimit)
	if err != nil {
		return nil, fmt.Errorf("%w: gas_limit", ErrEnvParseField)
	}
	timeSec, err := parseUint64(env.TimestampSeconds)
	if err != nil {
		return nil, fmt.Errorf("%w: timestamp_seconds", ErrEnvParseField)
	}
	timeMs, err := parseUint64(env.TimestampMs)
	if err != nil {
		return nil, fmt.Errorf("%w: timestamp_ms", ErrEnvParseField)
	}

	// 3) Parent linkage (deferred by the pure core): this is an N+1 sim on THIS parent.
	if !equalHashHex(env.ParentHash, x.parent.Hash()) {
		return nil, ErrEnvParentHashMismatch
	}
	wantNum := new(big.Int).Add(x.parent.Number, big.NewInt(1))
	if number.Cmp(wantNum) != 0 {
		return nil, ErrEnvNotParentPlusOne
	}

	canonical, err := x.prepareBlockEnv(env)
	if err != nil {
		return nil, err
	}
	if canonical.ForkRulesDigest != env.ForkRulesDigest {
		return nil, ErrEnvRulesMismatch
	}
	if !reflect.DeepEqual(canonical.BaseFee, env.BaseFee) {
		return nil, ErrEnvBaseFeeMismatch
	}
	if !reflect.DeepEqual(canonical, env) {
		return nil, ErrEnvDerivedMismatch
	}
	header, err := wireEnvHeader(env)
	if err != nil {
		return nil, err
	}
	baseFee := header.BaseFee

	return &resolvedBlockEnv{
		header:     header,
		number:     number,
		timeSec:    timeSec,
		timeMs:     timeMs,
		author:     common.HexToAddress(env.Author),
		gasLimit:   gasLimit,
		difficulty: difficulty,
		baseFee:    baseFee,
	}, nil
}

// parseUint64 parses a clean decimal string (already isUintStr-validated upstream) into
// a uint64, erroring on overflow rather than silently wrapping.
func parseUint64(s string) (uint64, error) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || !v.IsUint64() {
		return 0, ErrEnvParseField
	}
	return v.Uint64(), nil
}

// equalHashHex compares a lowercase 0x-hex hash string to a common.Hash without
// allocating a new string form (Hash.Hex lowercases, so a direct compare is fine).
func equalHashHex(hex string, h common.Hash) bool {
	return hex == h.Hex()
}

// Node-owned fingerprint. The exact JSON bytes are length-framed with the target
// number/time/merge flag; clients treat the returned hash as opaque. Config plus
// Rules binds fork activation, fee schedules and BSC-specific preamble decisions.
func (x *targetExecutor) forkRulesDigest(number *big.Int, sec uint64, merge bool) (string, error) {
	cfg, err := json.Marshal(x.chain.Config())
	if err != nil {
		return "", err
	}
	rules, err := json.Marshal(x.chain.Config().Rules(number, merge, sec))
	if err != nil {
		return "", err
	}
	var n, t [8]byte
	binary.BigEndian.PutUint64(n[:], number.Uint64())
	binary.BigEndian.PutUint64(t[:], sec)
	m := byte(0)
	if merge {
		m = 1
	}
	d := arb.NewDigest("arb.fork_rules.v3").FieldBytes(cfg).FieldBytes(rules).FieldBytes(n[:]).FieldBytes(t[:]).FieldBytes([]byte{m}).Finalize()
	return common.Hash(d).Hex(), nil
}

// Prepare fills node-derived fields, never changes the caller's execution
// assumptions. This is a hypothetical child header, not a consensus/seal proof.
func (x *targetExecutor) prepareBlockEnv(env arb.BlockEnv) (arb.BlockEnv, error) {
	if _, err := blockEnvDigestV3(env); err != nil {
		return env, err
	}
	h, err := wireEnvHeader(env)
	if err != nil {
		return env, err
	}
	if h.ParentHash != x.parent.Hash() {
		return env, ErrEnvParentHashMismatch
	}
	if !h.Number.IsUint64() || x.parent.Number.Uint64() == ^uint64(0) || h.Number.Uint64() != x.parent.Number.Uint64()+1 {
		return env, ErrEnvNotParentPlusOne
	}
	cfg := x.chain.Config()
	ms, _ := parseUint64(env.TimestampMs)
	if cfg.IsInBSC() {
		mix := new(big.Int).SetBytes(h.MixDigest[:])
		if !mix.IsUint64() || mix.Uint64() >= 1000 || mix.Uint64() != ms%1000 || (!cfg.IsLorentz(h.Number, h.Time) && mix.Sign() != 0) {
			return env, ErrEnvDerivedMismatch
		}
		if h.Difficulty.Cmp(big.NewInt(1)) != 0 && h.Difficulty.Cmp(big.NewInt(2)) != 0 {
			return env, ErrEnvDerivedMismatch
		}
		if ms <= x.parent.MilliTimestamp() {
			return env, ErrEnvDerivedMismatch
		}
	} else if h.Time <= x.parent.Time || ms%1000 != 0 {
		return env, ErrEnvDerivedMismatch
	}
	// Conservative child gas bound valid on both sides of BSC Lorentz.
	delta := h.GasLimit
	if delta > x.parent.GasLimit {
		delta -= x.parent.GasLimit
	} else {
		delta = x.parent.GasLimit - delta
	}
	if h.GasLimit > uint64(0x7fffffffffffffff) || h.GasLimit < params.MinGasLimit || delta >= x.parent.GasLimit/params.GasLimitBoundDivisor {
		return env, ErrEnvDerivedMismatch
	}
	env.BaseFee = nil
	if cfg.IsLondon(h.Number) {
		v := eip1559.CalcBaseFee(cfg, x.parent).String()
		env.BaseFee = &v
	}
	if cfg.IsCancun(h.Number, h.Time) {
		if eip4844.MaxBlobsPerBlock(cfg, h.Time) == 0 {
			return env, ErrEnvDerivedMismatch
		}
		v := strconv.FormatUint(eip4844.CalcExcessBlobGas(cfg, x.parent, h.Time), 10)
		env.ExcessBlobGas = &v
		if env.BlobGasUsed == nil {
			z := "0"
			env.BlobGasUsed = &z
		}
		h, _ = wireEnvHeader(env)
		if err := eip4844.VerifyEIP4844Header(cfg, x.parent, h); err != nil {
			return env, err
		}
	} else if env.BlobGasUsed != nil || env.ExcessBlobGas != nil {
		return env, ErrEnvDerivedMismatch
	}
	if cfg.IsInBSC() {
		env.ParentBeaconRoot = nil
		if cfg.IsBohr(h.Number, h.Time) {
			z := common.Hash{}.Hex()
			env.ParentBeaconRoot = &z
		}
	} else if cfg.IsCancun(h.Number, h.Time) {
		if env.ParentBeaconRoot == nil {
			return env, ErrEnvDerivedMismatch
		}
	} else if env.ParentBeaconRoot != nil {
		return env, ErrEnvDerivedMismatch
	}
	env.ForkRulesDigest, err = x.forkRulesDigest(h.Number, h.Time, h.Difficulty.Sign() == 0)
	return env, err
}

func wireEnvHeader(env arb.BlockEnv) (*types.Header, error) {
	if _, err := blockEnvDigestV3(env); err != nil {
		return nil, err
	}
	num, _ := new(big.Int).SetString(env.Number, 10)
	diff, _ := new(big.Int).SetString(env.Difficulty, 10)
	sec, _ := parseUint64(env.TimestampSeconds)
	gas, _ := parseUint64(env.GasLimit)
	extra, err := hexutil.Decode(env.ExtraData)
	if err != nil {
		return nil, err
	}
	if len(extra) > 32*1024 {
		return nil, ErrEnvDerivedMismatch
	}
	h := &types.Header{Number: num, ParentHash: common.HexToHash(env.ParentHash), Time: sec, Coinbase: common.HexToAddress(env.Author), GasLimit: gas, Difficulty: diff, MixDigest: common.HexToHash(env.MixDigest), Extra: extra}
	if env.BaseFee != nil {
		h.BaseFee, _ = new(big.Int).SetString(*env.BaseFee, 10)
	}
	if env.BlobGasUsed != nil {
		v, _ := parseUint64(*env.BlobGasUsed)
		h.BlobGasUsed = &v
	}
	if env.ExcessBlobGas != nil {
		v, _ := parseUint64(*env.ExcessBlobGas)
		h.ExcessBlobGas = &v
	}
	if env.ParentBeaconRoot != nil {
		v := common.HexToHash(*env.ParentBeaconRoot)
		h.ParentBeaconRoot = &v
	}
	return h, nil
}

type PrepareBlockEnvArgs struct {
	SchemaVersion string       `json:"schema_version"`
	BlockEnv      arb.BlockEnv `json:"block_env"`
}
type PrepareBlockEnvResult struct {
	Boot         string       `json:"boot"`
	BlockEnv     arb.BlockEnv `json:"block_env"`
	BlockEnvHash string       `json:"block_env_hash"`
}

func (api *ArbAPI) PrepareBlockEnv(args PrepareBlockEnvArgs) (*PrepareBlockEnvResult, error) {
	if args.SchemaVersion != "3" {
		return nil, ErrEnvDerivedMismatch
	}
	if err := arb.ValidateBlockEnv(args.BlockEnv); err != nil {
		return nil, err
	}
	h := api.svc.eth.blockchain.GetHeaderByHash(common.HexToHash(args.BlockEnv.ParentHash))
	if h == nil {
		return nil, errUnknownParentHash
	}
	x := &targetExecutor{chain: api.svc.eth.blockchain, parent: h}
	env, err := x.prepareBlockEnv(args.BlockEnv)
	if err != nil {
		return nil, err
	}
	digest, err := blockEnvDigestV3(env)
	if err != nil {
		return nil, err
	}
	return &PrepareBlockEnvResult{Boot: api.svc.boot, BlockEnv: env, BlockEnvHash: common.Hash(digest).Hex()}, nil
}
