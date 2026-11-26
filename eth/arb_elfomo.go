package eth

import (
	"bytes"
	"errors"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

var elfomoHelper = common.HexToAddress("0x78015e3544d989f0712a0b9986cb05838c3fe06d")
var elfomoTokens = [2]common.Address{common.HexToAddress("0x2170ed0880ac9a755fd29b2688956bd959f933f8"), common.HexToAddress("0x55d398326f99059ff775485246999027b3197955")}

type GetElfomoBookArgs struct {
	ParentHash   string       `json:"parent_hash"`
	RequestID    string       `json:"request_id"`
	PostHandle   string       `json:"post_handle"`
	Token0       string       `json:"token0,omitempty"`
	Token1       string       `json:"token1,omitempty"`
	BlockEnv     arb.BlockEnv `json:"block_env"`
	BudgetMicros uint64       `json:"budget_us"`
}

type GetElfomoBookResult struct {
	Boot       string `json:"boot"`
	ParentHash string `json:"parent_hash"`
	EnvHash    string `json:"env_hash"`
	Data       string `json:"data"`
}

func resolveElfomoTokens(token0Raw, token1Raw string) ([2]common.Address, error) {
	t0s := strings.TrimSpace(token0Raw)
	t1s := strings.TrimSpace(token1Raw)
	if t0s == "" && t1s == "" {
		return elfomoTokens, nil
	}
	if !common.IsHexAddress(t0s) || !common.IsHexAddress(t1s) {
		return [2]common.Address{}, errors.New("arb: invalid Elfomo token pair")
	}
	t0 := common.HexToAddress(t0s)
	t1 := common.HexToAddress(t1s)
	if t0 == (common.Address{}) || t1 == (common.Address{}) || bytes.Compare(t0.Bytes(), t1.Bytes()) >= 0 {
		return [2]common.Address{}, errors.New("arb: invalid Elfomo token pair")
	}
	return [2]common.Address{t0, t1}, nil
}

func readElfomoBook(c *poolCaller, tokens [2]common.Address) ([]byte, error) {
	data := crypto.Keccak256([]byte("getOrderbook(address,address)"))[:4]
	for _, token := range tokens {
		data = append(data, common.LeftPadBytes(token.Bytes(), 32)...)
	}
	return c.staticCall(elfomoHelper, data)
}

// Both parent and post-handle reads use the exact prepared child environment.
func (api *ArbAPI) GetElfomoBook(args GetElfomoBookArgs) (*GetElfomoBookResult, error) {
	if args.BudgetMicros == 0 || !isID32Wire(args.ParentHash) {
		return nil, errors.New("arb: invalid Elfomo request")
	}
	tokens, err := resolveElfomoTokens(args.Token0, args.Token1)
	if err != nil {
		return nil, err
	}
	budget := newReadBudget(defaultMaxReads, microsToDuration(args.BudgetMicros))
	var caller *poolCaller
	var parent *types.Header
	var envHash string
	if args.PostHandle != "" {
		if !isID32Wire(args.RequestID) || !isID32Wire(args.PostHandle) {
			return nil, errors.New("arb: invalid Elfomo handle")
		}
		identity := arb.HandleIdentity{OwnerSession: args.RequestID, NodeBootID: api.svc.boot, Identity: args.RequestID}
		base, p, release, err := api.svc.borrowBase(args.PostHandle, identity)
		if err != nil {
			return nil, err
		}
		defer release()
		if !strings.EqualFold(p.Hash().Hex(), args.ParentHash) {
			return nil, errors.New("arb: Elfomo parent mismatch")
		}
		env, err := newTargetExecutor(api.svc.eth.blockchain, p, base).resolveBlockEnv(args.BlockEnv)
		if err != nil {
			return nil, err
		}
		digest, err := blockEnvDigestV3(args.BlockEnv)
		if err != nil {
			return nil, err
		}
		envHash = hexutil.Encode(digest[:])
		parent = p
		caller = api.svc.eth.newPoolCaller(base, env.header, budget)
	} else {
		chain := api.svc.eth.blockchain
		parent = chain.GetHeaderByHash(common.HexToHash(args.ParentHash))
		if parent == nil {
			return nil, errors.New("arb: Elfomo parent unavailable")
		}
		base, err := chain.StateAt(parent)
		if err != nil {
			return nil, err
		}
		env, err := newTargetExecutor(chain, parent, base).resolveBlockEnv(args.BlockEnv)
		if err != nil {
			return nil, err
		}
		digest, err := blockEnvDigestV3(args.BlockEnv)
		if err != nil {
			return nil, err
		}
		envHash = hexutil.Encode(digest[:])
		caller = api.svc.eth.newPoolCaller(base, env.header, budget)
	}
	result, err := readElfomoBook(caller, tokens)
	if err != nil {
		return nil, err
	}
	if err := budget.CheckDeadline(); err != nil {
		return nil, err
	}
	return &GetElfomoBookResult{Boot: api.svc.boot, ParentHash: strings.ToLower(parent.Hash().Hex()), EnvHash: envHash, Data: hexutil.Encode(result)}, nil
}
