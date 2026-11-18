package eth

import (
	"errors"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

var fluxPoolEntry = common.HexToAddress("0xff7fe6b6951afd81bc5ef9d205c28e5117012fd8")
var fluxPoolRouter = common.HexToAddress("0xc59086c8ac9e440f68efb13d1abb2ccbef3df4d1")
var fluxPoolVault = common.HexToAddress("0x0f8e0136f09e8b188d21eddf17f65522f81f7151")
var fluxPoolFeeManager = common.HexToAddress("0x25ba19c2971c901aed02c184a69a9ec311ac41f2")
var fluxPoolRegistry = common.HexToAddress("0x00000000000004533fe15556b1e086bb1a72ceae")
var fluxPoolID = common.HexToHash("0xa3b94efb3bde9749d7e2735e0dd4a4c9a7b502bb3c6c44ae30fd35b2563038ae")
var fluxPoolTokens = [2]common.Address{common.HexToAddress("0x55d398326f99059ff775485246999027b3197955"), common.HexToAddress("0xbb4cdb9cbd36b01bd1cbaebf2de08d9173bc095c")}

func readFluxPoolState(c *poolCaller) ([]byte, error) {
	if c.wrapped.GetCodeHash(fluxPoolRouter) != common.HexToHash("0x3ea0e37498544aea8437f5a506a308f49f7a2f8f8c7927bf95c1e8163bf16129") ||
		common.BytesToAddress(c.wrapped.GetState(fluxPoolEntry, common.HexToHash("0x1")).Bytes()) != fluxPoolRouter ||
		common.BytesToAddress(c.wrapped.GetState(fluxPoolEntry, common.HexToHash("0x2")).Bytes()) != fluxPoolVault ||
		common.BytesToAddress(c.wrapped.GetState(fluxPoolRouter, common.HexToHash("0xd")).Bytes()) != fluxPoolFeeManager {
		return nil, errors.New("arb: unsupported FluxPool deployment")
	}
	registryData := append(crypto.Keccak256([]byte("ownerOf(uint256)"))[:4], common.LeftPadBytes([]byte{2}, 32)...)
	settlerData, err := c.staticCall(fluxPoolRegistry, registryData)
	if err != nil {
		return nil, err
	}
	if len(settlerData) != 32 {
		return nil, errors.New("arb: FluxPool settler identity")
	}
	settler := common.BytesToAddress(settlerData)
	// The production pricing router is the FeeManager's authorized reader.
	c.caller = fluxPoolRouter
	feeData := append(common.FromHex("0x9a8f07fc"), fluxPoolID.Bytes()...)
	feeData = append(feeData, common.LeftPadBytes(settler.Bytes(), 32)...)
	fee, err := c.staticCall(fluxPoolFeeManager, feeData)
	if err != nil {
		return nil, err
	}
	if len(fee) != 32 || new(big.Int).SetBytes(fee).Cmp(big.NewInt(1_000_000)) >= 0 {
		return nil, errors.New("arb: FluxPool taker fee")
	}
	data := append(common.LeftPadBytes(c.evm.Context.BlockNumber.Bytes(), 32), common.LeftPadBytes(new(big.Int).SetUint64(c.evm.Context.Time).Bytes(), 32)...)
	data = append(data, settlerData...)
	data = append(data, fee...)
	for _, owner := range []common.Address{fluxPoolVault, settler} {
		for _, token := range fluxPoolTokens {
			balanceData := append(crypto.Keccak256([]byte("balanceOf(address)"))[:4], common.LeftPadBytes(owner.Bytes(), 32)...)
			balance, err := c.staticCall(token, balanceData)
			if err != nil {
				return nil, err
			}
			if len(balance) != 32 {
				return nil, errors.New("arb: FluxPool token balance")
			}
			data = append(data, balance...)
		}
	}
	allowed := crypto.Keccak256Hash(common.LeftPadBytes(settler.Bytes(), 32), common.HexToHash("0x4").Bytes())
	ready := byte(0)
	control := c.wrapped.GetState(fluxPoolVault, common.HexToHash("0x2"))
	// Global/swap pause share the entry address's word; withdrawal pause is separate.
	if control[10] == 0 && control[11] == 0 && common.BytesToAddress(control.Bytes()) == fluxPoolEntry && c.wrapped.GetState(fluxPoolVault, allowed) != (common.Hash{}) {
		ready = 1
	}
	for _, token := range fluxPoolTokens {
		slot := crypto.Keccak256Hash(common.LeftPadBytes(token.Bytes(), 32), common.HexToHash("0x3").Bytes())
		if c.wrapped.GetState(fluxPoolVault, slot) == (common.Hash{}) {
			ready = 0
		}
	}
	data = append(data, common.LeftPadBytes([]byte{ready}, 32)...)
	data = append(data, c.wrapped.GetCodeHash(settler).Bytes()...)
	base := new(big.Int).SetBytes(crypto.Keccak256(fluxPoolID.Bytes(), common.HexToHash("0x7").Bytes()))
	for i := int64(0); i < 11; i++ {
		slot := common.BigToHash(new(big.Int).Add(base, big.NewInt(i)))
		data = append(data, c.wrapped.GetState(fluxPoolRouter, slot).Bytes()...)
	}
	if c.wrapped.Budget().Failed() {
		return nil, ErrReadBudgetTripped
	}
	return data, nil
}

type GetFluxPoolStateArgs struct {
	ParentHash   string       `json:"parent_hash"`
	RequestID    string       `json:"request_id"`
	PostHandle   string       `json:"post_handle"`
	BlockEnv     arb.BlockEnv `json:"block_env"`
	BudgetMicros uint64       `json:"budget_us"`
}

type GetFluxPoolStateResult struct {
	Boot       string `json:"boot"`
	ParentHash string `json:"parent_hash"`
	EnvHash    string `json:"env_hash"`
	Data       string `json:"data"`
}

// Both parent and post-handle reads use the exact prepared child environment.
func (api *ArbAPI) GetFluxPoolState(args GetFluxPoolStateArgs) (*GetFluxPoolStateResult, error) {
	if args.BudgetMicros == 0 || !isID32Wire(args.ParentHash) {
		return nil, errors.New("arb: invalid FluxPool request")
	}
	budget := newReadBudget(defaultMaxReads, microsToDuration(args.BudgetMicros))
	var caller *poolCaller
	var parent *types.Header
	var envHash string
	if args.PostHandle != "" {
		if !isID32Wire(args.RequestID) || !isID32Wire(args.PostHandle) {
			return nil, errors.New("arb: invalid FluxPool handle")
		}
		identity := arb.HandleIdentity{OwnerSession: args.RequestID, NodeBootID: api.svc.boot, Identity: args.RequestID}
		base, p, release, err := api.svc.borrowBase(args.PostHandle, identity)
		if err != nil {
			return nil, err
		}
		defer release()
		if !strings.EqualFold(p.Hash().Hex(), args.ParentHash) {
			return nil, errors.New("arb: FluxPool parent mismatch")
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
			return nil, errors.New("arb: FluxPool parent unavailable")
		}
		base, err := chain.StateAt(parent)
		if err != nil {
			return nil, err
		}
		if args.BlockEnv.Number == "" {
			child := types.CopyHeader(parent)
			child.Number = new(big.Int).Add(parent.Number, big.NewInt(1))
			caller = api.svc.eth.newPoolCaller(base, child, budget)
		} else {
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
	}
	result, err := readFluxPoolState(caller)
	if err != nil {
		return nil, err
	}
	if err := budget.CheckDeadline(); err != nil {
		return nil, err
	}
	return &GetFluxPoolStateResult{Boot: api.svc.boot, ParentHash: strings.ToLower(parent.Hash().Hex()), EnvHash: envHash, Data: hexutil.Encode(result)}, nil
}
