package eth

import (
	"errors"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
)

var prismRouter = common.HexToAddress("0xc2a6300eeaa828c8cf6e5906e65ea0f9b253c956")

type PrismPair struct {
	Token0 string `json:"token0"`
	Token1 string `json:"token1"`
}

type GetPrismBooksArgs struct {
	RequestID    string       `json:"request_id"`
	PostHandle   string       `json:"post_handle"`
	BlockEnv     arb.BlockEnv `json:"block_env"`
	Adapter      string       `json:"adapter"`
	Sender       string       `json:"sender"`
	GasPrice     string       `json:"gas_price"`
	Pairs        []PrismPair  `json:"pairs"`
	BudgetMicros uint64       `json:"budget_us"`
}

type PrismBookResult struct {
	Data  string `json:"data"`
	Error string `json:"error,omitempty"`
}

type GetPrismBooksResult struct {
	Boot       string            `json:"boot"`
	ParentHash string            `json:"parent_hash"`
	EnvHash    string            `json:"env_hash"`
	Books      []PrismBookResult `json:"books"`
}

func validatePrismBooks(args *GetPrismBooksArgs) (*big.Int, error) {
	if !isID32Wire(args.RequestID) || !isID32Wire(args.PostHandle) || args.BudgetMicros == 0 ||
		!isAddressWire(args.Adapter) || !isAddressWire(args.Sender) {
		return nil, errors.New("arb: invalid Prism book request")
	}
	price, ok := new(big.Int).SetString(args.GasPrice, 10)
	if !ok || price.Sign() < 0 || price.BitLen() > 256 {
		return nil, errors.New("arb: invalid Prism gas price")
	}
	for _, pair := range args.Pairs {
		if !isAddressWire(pair.Token0) || !isAddressWire(pair.Token1) || pair.Token0 >= pair.Token1 {
			return nil, errors.New("arb: invalid Prism pair")
		}
	}
	return price, nil
}

func prismBookData(pair PrismPair, args *GetPrismBooksArgs, gasPrice *big.Int) []byte {
	data := crypto.Keccak256([]byte("getOrderBookWithParams(address,address,(address,address,uint256,uint8))"))[:4]
	for _, address := range []string{pair.Token0, pair.Token1, args.Adapter, args.Sender} {
		data = append(data, common.LeftPadBytes(common.HexToAddress(address).Bytes(), 32)...)
	}
	data = append(data, common.LeftPadBytes(gasPrice.Bytes(), 32)...)
	return append(data, make([]byte, 32)...)
}

// GetPrismBooks reads the borrowed post-target state in the exact prepared
// child environment. StaticCall and a private state copy keep the handle intact.
func (api *ArbAPI) GetPrismBooks(args GetPrismBooksArgs) (*GetPrismBooksResult, error) {
	gasPrice, err := validatePrismBooks(&args)
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
	books := make([]PrismBookResult, len(args.Pairs))
	for i, pair := range args.Pairs {
		raw, readErr := caller.staticCall(prismRouter, prismBookData(pair, &args, gasPrice))
		if readErr != nil {
			books[i].Error = readErr.Error()
		} else {
			books[i].Data = hexutil.Encode(raw)
		}
		if err := budget.CheckDeadline(); err != nil {
			return nil, err
		}
	}
	hash, err := blockEnvDigestV3(args.BlockEnv)
	if err != nil {
		return nil, err
	}
	return &GetPrismBooksResult{Boot: api.svc.boot, ParentHash: strings.ToLower(parent.Hash().Hex()),
		EnvHash: hexutil.Encode(hash[:]), Books: books}, nil
}
