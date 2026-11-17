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

type GetListaLpStateArgs struct {
	RequestID    string       `json:"request_id"`
	PostHandle   string       `json:"post_handle"`
	BlockEnv     arb.BlockEnv `json:"block_env"`
	Sender       string       `json:"sender"`
	GasPrice     string       `json:"gas_price"`
	Adapter      string       `json:"adapter"`
	BudgetMicros uint64       `json:"budget_us"`
}

type GetListaLpStateResult struct {
	Boot       string `json:"boot"`
	ParentHash string `json:"parent_hash"`
	EnvHash    string `json:"env_hash"`
	Data       string `json:"data"`
}

func validateListaLpState(args *GetListaLpStateArgs) (*big.Int, error) {
	if !isID32Wire(args.RequestID) || !isID32Wire(args.PostHandle) || !isAddressWire(args.Sender) ||
		!isAddressWire(args.Adapter) || args.BudgetMicros == 0 {
		return nil, errors.New("arb: invalid Lista LP request")
	}
	price, ok := new(big.Int).SetString(args.GasPrice, 10)
	if !ok || price.Sign() < 0 || price.BitLen() > 256 {
		return nil, errors.New("arb: invalid Lista LP gas price")
	}
	return price, nil
}

func (api *ArbAPI) GetListaLpState(args GetListaLpStateArgs) (*GetListaLpStateResult, error) {
	price, err := validateListaLpState(&args)
	if err != nil {
		return nil, err
	}
	identity := arb.HandleIdentity{OwnerSession: args.RequestID, NodeBootID: api.svc.boot, Identity: args.RequestID}
	base, parent, release, err := api.svc.borrowBase(args.PostHandle, identity)
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
	caller.evm.SetTxContext(vm.TxContext{Origin: common.HexToAddress(args.Sender), GasPrice: uint256.MustFromBig(price)})
	data, err := isolatedPoolCall(caller, common.HexToAddress(args.Adapter), crypto.Keccak256([]byte("readState()"))[:4])
	if err != nil {
		return nil, err
	}
	if err := budget.CheckDeadline(); err != nil {
		return nil, err
	}
	hash, err := blockEnvDigestV3(args.BlockEnv)
	if err != nil {
		return nil, err
	}
	return &GetListaLpStateResult{Boot: api.svc.boot, ParentHash: strings.ToLower(parent.Hash().Hex()), EnvHash: hexutil.Encode(hash[:]), Data: hexutil.Encode(data)}, nil
}
