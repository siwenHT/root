package eth

import (
	"errors"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
	"github.com/holiman/uint256"
)

var metricDataProvider = common.HexToAddress("0x9bb15fe2b214e37d19a6c59ee7b132e787fdfb2a")

type GetMetricBooksArgs struct {
	RequestID    string       `json:"request_id"`
	PostHandle   string       `json:"post_handle"`
	BlockEnv     arb.BlockEnv `json:"block_env"`
	Sender       string       `json:"sender"`
	GasPrice     string       `json:"gas_price"`
	Pools        []string     `json:"pools"`
	BudgetMicros uint64       `json:"budget_us"`
}

type MetricBookResult struct {
	Immutable string `json:"immutable"`
	Data      string `json:"data"`
	Slot0     string `json:"slot0"`
	Error     string `json:"error,omitempty"`
}

type GetMetricBooksResult struct {
	Boot       string             `json:"boot"`
	ParentHash string             `json:"parent_hash"`
	EnvHash    string             `json:"env_hash"`
	Books      []MetricBookResult `json:"books"`
}

func validateMetricBooks(args *GetMetricBooksArgs) (*big.Int, error) {
	if !isID32Wire(args.RequestID) || !isID32Wire(args.PostHandle) || !isAddressWire(args.Sender) || args.BudgetMicros == 0 {
		return nil, errors.New("arb: invalid Metric book request")
	}
	price, ok := new(big.Int).SetString(args.GasPrice, 10)
	if !ok || price.Sign() < 0 || price.BitLen() > 256 {
		return nil, errors.New("arb: invalid Metric gas price")
	}
	for _, pool := range args.Pools {
		if !isAddressWire(pool) {
			return nil, errors.New("arb: invalid Metric pool")
		}
	}
	return price, nil
}

func metricCall(c *poolCaller, target common.Address, data []byte) ([]byte, error) {
	// Metric's view helpers use transient storage to expose their oracle context.
	// Call on the reader's private copy, reverting even successful helper writes.
	snapshot := c.wrapped.Snapshot()
	defer c.wrapped.RevertToSnapshot(snapshot)
	result, _, err := c.evm.Call(c.caller, target, data, c.gasCap, uint256.NewInt(0))
	if c.wrapped.Budget().Failed() {
		return nil, ErrReadBudgetTripped
	}
	if err != nil {
		return nil, ErrViewCallReverted
	}
	return result, nil
}

func readMetricBook(caller *poolCaller, pool common.Address) (MetricBookResult, error) {
	immutable, err := metricCall(caller, pool, crypto.Keccak256([]byte("getImmutables()"))[:4])
	if err != nil {
		return MetricBookResult{}, err
	}
	data := crypto.Keccak256([]byte("getLiquidityDepthLive(address,uint8)"))[:4]
	data = append(data, common.LeftPadBytes(pool.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes([]byte{255}, 32)...)
	depth, err := metricCall(caller, metricDataProvider, data)
	if err != nil {
		return MetricBookResult{}, err
	}
	data = crypto.Keccak256([]byte("slot0(address)"))[:4]
	data = append(data, common.LeftPadBytes(pool.Bytes(), 32)...)
	slot, err := metricCall(caller, metricDataProvider, data)
	if err != nil {
		return MetricBookResult{}, err
	}
	return MetricBookResult{Immutable: hexutil.Encode(immutable), Data: hexutil.Encode(depth), Slot0: hexutil.Encode(slot)}, nil
}

func (api *ArbAPI) GetMetricBooks(args GetMetricBooksArgs) (*GetMetricBooksResult, error) {
	price, err := validateMetricBooks(&args)
	if err != nil {
		return nil, err
	}
	ident := arb.HandleIdentity{OwnerSession: args.RequestID, NodeBootID: api.svc.boot, Identity: args.RequestID}
	base, parent, release, err := api.svc.borrowBase(args.PostHandle, ident)
	if err != nil {
		return nil, err
	}
	defer release()
	x := newTargetExecutor(api.svc.eth.blockchain, parent, base)
	env, err := x.resolveBlockEnv(args.BlockEnv)
	if err != nil {
		return nil, err
	}
	budget := newReadBudget(defaultMaxReads, microsToDuration(args.BudgetMicros))
	caller := api.svc.eth.newPoolCaller(base, env.header, budget)
	caller.evm.SetTxContext(vm.TxContext{Origin: common.HexToAddress(args.Sender), GasPrice: price})
	books := make([]MetricBookResult, len(args.Pools))
	for i, pool := range args.Pools {
		book, readErr := readMetricBook(caller, common.HexToAddress(pool))
		if readErr != nil {
			book.Error = readErr.Error()
		}
		books[i] = book
		if err := budget.CheckDeadline(); err != nil {
			return nil, err
		}
		if budget.Failed() {
			return nil, budget.Err()
		}
	}
	hash, err := blockEnvDigestV3(args.BlockEnv)
	if err != nil {
		return nil, err
	}
	return &GetMetricBooksResult{Boot: api.svc.boot, ParentHash: strings.ToLower(parent.Hash().Hex()), EnvHash: hexutil.Encode(hash[:]), Books: books}, nil
}
