package eth

import (
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/arb"
	"math/big"
	"strings"
)

type GetEllipsisStateArgs struct {
	ParentHash   string       `json:"parent_hash"`
	RequestID    string       `json:"request_id"`
	PostHandle   string       `json:"post_handle"`
	BlockEnv     arb.BlockEnv `json:"block_env"`
	BudgetMicros uint64       `json:"budget_us"`
	Pool         string       `json:"pool"`
}

type GetEllipsisStateResult struct {
	Boot       string                 `json:"boot"`
	ParentHash string                 `json:"parent_hash"`
	EnvHash    string                 `json:"env_hash"`
	Timestamp  uint64                 `json:"timestamp"`
	Stable     map[string]interface{} `json:"stable"`
	Base       map[string]interface{} `json:"base"`
}

var ellipsisOldBase = common.HexToAddress("0x160caed03795365f3a589f10c379ffa7d75d4e76")
var ellipsisValBase = common.HexToAddress("0x19ec9e3f7b21dd27598e7ad5aae7dc0db00a806d")

func ellipsisBase(pool common.Address) common.Address {
	switch strings.ToLower(pool.Hex()) {
	case "0xedbb3f63c0901ba500e4525da0c2cbd27ac8ffdc":
		return ellipsisOldBase
	case "0xd3763ec7f4b0e6b3b28b497ba97226178b1e6249", "0x98a7f4c12e5f8447d59400c8e4f7470aea8ec056":
		return ellipsisValBase
	}
	return common.Address{}
}
func ellipsisSupported(pool common.Address) bool {
	return pool == ellipsisOldBase || pool == ellipsisValBase || ellipsisBase(pool) != (common.Address{})
}
func readEllipsisStable(c *poolCaller, pool common.Address) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	n := 2
	if pool == ellipsisOldBase || pool == ellipsisValBase {
		n = 3
	}
	for _, signature := range []string{"A()", "fee()", "admin_fee()", "get_virtual_price()"} {
		data, err := liquidFiCall(c, pool, signature)
		if err != nil {
			return nil, err
		}
		if len(data) != 32 {
			return nil, errors.New("arb: Ellipsis word length")
		}
		out[signature] = hexutil.Encode(data)
	}
	if pool != ellipsisOldBase {
		data, err := liquidFiCall(c, pool, "A_precise()")
		if err != nil {
			return nil, err
		}
		out["A_precise()"] = hexutil.Encode(data)
	}
	for _, signature := range []string{"coins(uint256)", "balances(uint256)"} {
		words := make([]string, n)
		for i := 0; i < n; i++ {
			data, err := liquidFiCall(c, pool, signature, big.NewInt(int64(i)).Bytes())
			if err != nil {
				return nil, err
			}
			words[i] = hexutil.Encode(data)
		}
		out[signature] = words
	}

	if pool == common.HexToAddress("0xedbb3f63c0901ba500e4525da0c2cbd27ac8ffdc") {
		for _, signature := range []string{"base_virtual_price()", "base_cache_updated()"} {
			data, err := liquidFiCall(c, pool, signature)
			if err != nil {
				return nil, err
			}
			out[signature] = hexutil.Encode(data)
		}
	}
	lp := common.HexToAddress("0xaf4de8e872131ae328ce21d909c74705d3aaf452").Bytes()
	var err error
	if pool != ellipsisOldBase {
		lp, err = liquidFiCall(c, pool, "lp_token()")
		if err != nil {
			return nil, err
		}
	}
	supply, err := liquidFiCall(c, common.BytesToAddress(lp), "totalSupply()")
	if err != nil {
		return nil, err
	}
	out["totalSupply()"] = hexutil.Encode(supply)
	if pool == ellipsisValBase {
		data, err := liquidFiCall(c, pool, "offpeg_fee_multiplier()")
		if err != nil {
			return nil, err
		}
		out["offpeg_fee_multiplier()"] = hexutil.Encode(data)
		available := make([]string, 3)
		deposits := make([]bool, 3)
		for i := 0; i < 3; i++ {
			index := big.NewInt(int64(i)).Bytes()
			token, err := liquidFiCall(c, pool, "coins(uint256)", index)
			if err != nil {
				return nil, err
			}
			wrapped, err := liquidFiCall(c, pool, "wrapped_coins(uint256)", index)
			if err != nil {
				return nil, err
			}
			cash, err := liquidFiCall(c, common.BytesToAddress(token), "balanceOf(address)", wrapped)
			if err != nil {
				return nil, err
			}
			available[i] = hexutil.Encode(cash)
			config, err := liquidFiCall(c, common.HexToAddress("0xe29a55a6aeff5c8b1beede5bcf2f0cb3af8f91f5"), "getConfiguration(address)", token)
			if err != nil {
				return nil, err
			}
			bits := new(big.Int).SetBytes(config)
			deposits[i] = bits.Bit(56) == 1 && bits.Bit(57) == 0
		}
		out["available"] = available
		out["deposit_enabled"] = deposits
	}
	return out, nil
}
func readEllipsisState(c *poolCaller, pool common.Address) (*GetEllipsisStateResult, error) {
	stable, err := readEllipsisStable(c, pool)
	if err != nil {
		return nil, err
	}
	result := &GetEllipsisStateResult{Stable: stable}
	if base := ellipsisBase(pool); base != (common.Address{}) {
		result.Base, err = readEllipsisStable(c, base)
		if err != nil {
			return nil, err
		}
	}
	if c.wrapped.Budget().Failed() {
		return nil, ErrReadBudgetTripped
	}
	return result, nil
}

func (api *ArbAPI) GetEllipsisState(args GetEllipsisStateArgs) (*GetEllipsisStateResult, error) {
	if args.BudgetMicros == 0 || !isID32Wire(args.ParentHash) || !isAddressWire(args.Pool) || !ellipsisSupported(common.HexToAddress(args.Pool)) {
		return nil, errors.New("arb: invalid Ellipsis request")
	}
	budget := newReadBudget(defaultMaxReads, microsToDuration(args.BudgetMicros))
	var caller *poolCaller
	var parent *types.Header
	var envHash string
	if args.PostHandle != "" {
		if !isID32Wire(args.RequestID) || !isID32Wire(args.PostHandle) {
			return nil, errors.New("arb: invalid Ellipsis handle")
		}
		identity := arb.HandleIdentity{OwnerSession: args.RequestID, NodeBootID: api.svc.boot, Identity: args.RequestID}
		base, p, release, err := api.svc.borrowBase(args.PostHandle, identity)
		if err != nil {
			return nil, err
		}
		defer release()
		if !strings.EqualFold(p.Hash().Hex(), args.ParentHash) {
			return nil, errors.New("arb: Ellipsis parent mismatch")
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
			return nil, errors.New("arb: Ellipsis parent unavailable")
		}
		base, err := chain.StateAt(parent.Root)
		if err != nil {
			return nil, err
		}
		child := types.CopyHeader(parent)
		child.Number = new(big.Int).Add(parent.Number, big.NewInt(1))
		if args.BlockEnv.ParentHash != "" {
			resolved, err := newTargetExecutor(chain, parent, base).resolveBlockEnv(args.BlockEnv)
			if err != nil {
				return nil, err
			}
			child = resolved.header
			digest, err := blockEnvDigestV3(args.BlockEnv)
			if err != nil {
				return nil, err
			}
			envHash = hexutil.Encode(digest[:])
		}
		caller = api.svc.eth.newPoolCaller(base, child, budget)
	}
	result, err := readEllipsisState(caller, common.HexToAddress(args.Pool))
	if err != nil {
		return nil, err
	}
	if err := budget.CheckDeadline(); err != nil {
		return nil, err
	}
	result.Boot = api.svc.boot
	result.ParentHash = strings.ToLower(parent.Hash().Hex())
	result.EnvHash = envHash
	result.Timestamp = caller.evm.Context.Time
	return result, nil
}
