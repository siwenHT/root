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

var liquidFiEntry = common.HexToAddress("0x114b11ae066a7785452c4570b42fff1b7fa5b001")
var liquidFiPricing = common.HexToAddress("0x19f45175e813338fc79e972601a0cec7182bddc8")
var liquidFiVault = common.HexToAddress("0x8d6fcf96253cd9c828b7c5d37ebcc1e1f0d7aa5e")
var liquidFiUSDT = common.HexToAddress("0x55d398326f99059ff775485246999027b3197955")
var liquidFiPairs = [2][2]common.Address{
	{liquidFiUSDT, common.HexToAddress("0xbb4cdb9cbd36b01bd1cbaebf2de08d9173bc095c")},
	{liquidFiUSDT, common.HexToAddress("0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d")},
}

type GetLiquidFiStateArgs struct {
	PairID       uint64       `json:"pair_id"`
	ParentHash   string       `json:"parent_hash"`
	RequestID    string       `json:"request_id"`
	PostHandle   string       `json:"post_handle"`
	BlockEnv     arb.BlockEnv `json:"block_env"`
	BudgetMicros uint64       `json:"budget_us"`
}

type GetLiquidFiStateResult struct {
	Boot           string    `json:"boot"`
	ParentHash     string    `json:"parent_hash"`
	EnvHash        string    `json:"env_hash"`
	Block          string    `json:"block"`
	Packed         string    `json:"packed"`
	Implementation string    `json:"implementation"`
	MaxInput       string    `json:"max_input"`
	Cumulative     string    `json:"cumulative"`
	BlockInput     string    `json:"block_input"`
	Balances       [2]string `json:"balances"`
	Paused         string    `json:"paused"`
}

func liquidFiCall(c *poolCaller, address common.Address, signature string, args ...[]byte) ([]byte, error) {
	data := crypto.Keccak256([]byte(signature))[:4]
	for _, arg := range args {
		data = append(data, common.LeftPadBytes(arg, 32)...)
	}
	return c.staticCall(address, data)
}

func readLiquidFiState(c *poolCaller, pairID uint64, tokens [2]common.Address) (*GetLiquidFiStateResult, error) {
	for _, item := range []struct {
		signature string
		expected  common.Address
	}{{"pricing()", liquidFiPricing}, {"vault()", liquidFiVault}} {
		data, err := liquidFiCall(c, liquidFiEntry, item.signature)
		if err != nil {
			return nil, err
		}
		if len(data) != 32 || common.BytesToAddress(data) != item.expected {
			return nil, errors.New("arb: LiquidFi entry identity")
		}
	}
	implSlot := common.HexToHash("0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc")
	pairWord := common.LeftPadBytes(new(big.Int).SetUint64(pairID).Bytes(), 32)
	priceSlot := crypto.Keccak256Hash(pairWord, common.FromHex("0x8759f18911d145bf318ed8280c3612d855b1eb13a1ec427ab54ed53d55d77d05"))
	result := &GetLiquidFiStateResult{Packed: c.wrapped.GetState(liquidFiPricing, priceSlot).Hex(), Implementation: common.BytesToAddress(c.wrapped.GetState(liquidFiPricing, implSlot).Bytes()).Hex()}
	blockRoot := crypto.Keccak256Hash(common.FromHex(result.Packed), common.HexToHash("0x8759f18911d145bf318ed8280c3612d855b1eb13a1ec427ab54ed53d55d77d0d").Bytes())
	blockSlot := crypto.Keccak256Hash(common.LeftPadBytes(c.evm.Context.BlockNumber.Bytes(), 32), blockRoot.Bytes())
	result.BlockInput = c.wrapped.GetState(liquidFiPricing, blockSlot).Hex()
	for _, item := range []struct {
		signature string
		args      [][]byte
		out       *string
	}{
		{"getPairGlobalConfig(uint256)", [][]byte{pairWord}, &result.MaxInput},
		{"getPairCumulatives(uint256)", [][]byte{pairWord}, &result.Cumulative},
		{"paused()", nil, &result.Paused},
	} {
		data, err := liquidFiCall(c, liquidFiPricing, item.signature, item.args...)
		if err != nil {
			return nil, err
		}
		*item.out = hexutil.Encode(data)
	}
	for i, token := range tokens {
		data, err := liquidFiCall(c, token, "balanceOf(address)", liquidFiVault.Bytes())
		if err != nil {
			return nil, err
		}
		result.Balances[i] = hexutil.Encode(data)
	}
	if c.wrapped.Budget().Failed() {
		return nil, ErrReadBudgetTripped
	}
	return result, nil
}

// Reads canonical parent or the already simulated target handle. Head reads use
// the child block number too, so epoch counters and expiries match execution.
func (api *ArbAPI) GetLiquidFiState(args GetLiquidFiStateArgs) (*GetLiquidFiStateResult, error) {
	pairID := args.PairID
	if pairID == 0 {
		pairID = 1
	}
	if args.BudgetMicros == 0 || !isID32Wire(args.ParentHash) || pairID > uint64(len(liquidFiPairs)) {
		return nil, errors.New("arb: invalid LiquidFi request")
	}
	budget := newReadBudget(defaultMaxReads, microsToDuration(args.BudgetMicros))
	var caller *poolCaller
	var parent *types.Header
	var envHash string
	if args.PostHandle != "" {
		if !isID32Wire(args.RequestID) || !isID32Wire(args.PostHandle) {
			return nil, errors.New("arb: invalid LiquidFi handle")
		}
		identity := arb.HandleIdentity{OwnerSession: args.RequestID, NodeBootID: api.svc.boot, Identity: args.RequestID}
		base, p, release, err := api.svc.borrowBase(args.PostHandle, identity)
		if err != nil {
			return nil, err
		}
		defer release()
		if !strings.EqualFold(p.Hash().Hex(), args.ParentHash) {
			return nil, errors.New("arb: LiquidFi parent mismatch")
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
			return nil, errors.New("arb: LiquidFi parent unavailable")
		}
		base, err := chain.StateAt(parent)
		if err != nil {
			return nil, err
		}
		child := types.CopyHeader(parent)
		child.Number = new(big.Int).Add(parent.Number, big.NewInt(1))
		caller = api.svc.eth.newPoolCaller(base, child, budget)
	}
	result, err := readLiquidFiState(caller, pairID, liquidFiPairs[pairID-1])
	if err != nil {
		return nil, err
	}
	if err := budget.CheckDeadline(); err != nil {
		return nil, err
	}
	result.Boot = api.svc.boot
	result.ParentHash = strings.ToLower(parent.Hash().Hex())
	result.EnvHash = envHash
	result.Block = caller.evm.Context.BlockNumber.String()
	return result, nil
}
