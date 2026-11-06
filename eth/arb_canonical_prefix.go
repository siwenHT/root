package eth

import (
	"bytes"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/arb"
)

type ReplayCanonicalPrefixArgs struct {
	RequestID    string `json:"request_id"`
	Stamp        string `json:"stamp"`
	ParentHandle string `json:"parent_handle"`
	BlockHash    string `json:"block_hash"`
	StopIndex    uint64 `json:"stop_index"`
	BudgetMicros uint64 `json:"budget_us"`
}

// ReplayCanonicalPrefix preserves included reverts' fees and nonces. Bundle
// simulation still stops on a failed target; this separate method is audit-only.
func (api *ArbAPI) ReplayCanonicalPrefix(args ReplayCanonicalPrefixArgs) (*JobIDResult, error) {
	ident, err := api.commonSimValidate(args.RequestID, args.Stamp, args.ParentHandle, args.BudgetMicros)
	if err != nil {
		return nil, err
	}
	if !isHash32Wire(args.BlockHash) {
		return nil, errBadParentHash
	}
	chain := api.svc.eth.blockchain
	block := chain.GetBlockByHash(common.HexToHash(args.BlockHash))
	if block == nil || chain.GetCanonicalHash(block.NumberU64()) != block.Hash() || args.StopIndex > uint64(len(block.Transactions())) {
		return nil, errors.New("arb: canonical prefix block or index unavailable")
	}
	receipts := chain.GetReceiptsByHash(block.Hash())
	if len(receipts) != len(block.Transactions()) {
		return nil, errors.New("arb: canonical receipts unavailable")
	}
	digest := hex32Str(arb.NewDigest("arb.req.canonicalPrefix.v1").
		FieldBytes([]byte(args.ParentHandle)).FieldBytes([]byte(args.Stamp)).
		FieldBytes(block.Hash().Bytes()).
		FieldU64(args.StopIndex).FieldU64(args.BudgetMicros).Finalize())
	run := func(budget *arb.ReadBudget) (*arb.JobOutcome, bool) {
		base, header, release, err := api.svc.borrowBase(args.ParentHandle, ident)
		if err != nil {
			return &arb.JobOutcome{Kind: "canonical_prefix", ErrCode: err.Error()}, false
		}
		defer release()
		if header.Hash() != block.ParentHash() {
			return &arb.JobOutcome{Kind: "canonical_prefix", ErrCode: "canonical_block_env_mismatch"}, false
		}
		x := newTargetExecutor(chain, header, base)
		res, err := x.executeCanonicalPrefix(block, args.StopIndex, receipts, budget)
		if err != nil {
			return &arb.JobOutcome{Kind: "canonical_prefix", ErrCode: err.Error()}, false
		}
		out := api.mapPrefixOutcome(args.ParentHandle, ident, res, budget)
		out.Kind = "canonical_prefix"
		rows := make([]map[string]any, len(res.Outcomes))
		for i, tx := range res.Outcomes {
			rows[i] = map[string]any{"index": i, "tx": block.Transactions()[i].Hash().Hex(), "status": tx.Class.Status.String(), "gas_used": tx.UsedGas, "receipt_verified": res.Completed}
		}
		out.ExecutorPayload = map[string]any{"block_hash": args.BlockHash, "parent_hash": block.ParentHash().Hex(), "stop_index": args.StopIndex, "transactions": rows}
		if !res.Completed {
			out.ErrCode = "canonical_prefix_execution_mismatch"
		}
		return out, res.Completed && out.PostHandle != ""
	}
	job, err := api.submitAndEnqueue(arb.JobNamespace("arb_replayCanonicalPrefix"), args.RequestID, args.Stamp, digest, args.BudgetMicros, run)
	if err != nil {
		return nil, err
	}
	return &JobIDResult{JobID: job}, nil
}

func (x *targetExecutor) executeCanonicalPrefix(block *types.Block, stopIndex uint64, receipts types.Receipts, budget *arb.ReadBudget) (*PrefixResult, error) {
	header := block.Header()
	author, err := x.chain.Engine().Author(header)
	if err != nil {
		return nil, err
	}
	header.Coinbase = author
	s := x.newExecSession(budget, &resolvedBlockEnv{header: header})
	res := &PrefixResult{Budget: budget}
	for i, tx := range block.Transactions()[:stopIndex] {
		out := s.applyOne(tx, i)
		res.Outcomes = append(res.Outcomes, out)
		if !out.Class.ReceiptTrusted {
			return res, nil
		}
		// Consensus receipt bytes cover status, cumulative gas and all logs. The
		// native block processor supplies bloom asynchronously; this path does not.
		out.Receipt.Bloom = types.CreateBloom(out.Receipt)
		got, err := out.Receipt.MarshalBinary()
		if err != nil {
			return nil, err
		}
		want, err := receipts[i].MarshalBinary()
		if err != nil {
			return nil, err
		}
		if out.UsedGas != receipts[i].GasUsed || !bytes.Equal(got, want) {
			return res, nil
		}
	}
	res.Completed, res.PostState = true, s.work
	return res, nil
}

type GetAccountStateArgs struct {
	RequestID string `json:"request_id"`
	HandleID  string `json:"handle_id"`
	Address   string `json:"address"`
}

type GetAccountStateResult struct {
	Boot       string `json:"boot"`
	HandleID   string `json:"handle_id"`
	ParentHash string `json:"parent_hash"`
	Address    string `json:"address"`
	Nonce      uint64 `json:"nonce"`
	Balance    string `json:"balance_wei"`
	CodeHash   string `json:"code_hash"`
}

func (api *ArbAPI) GetAccountState(args GetAccountStateArgs) (*GetAccountStateResult, error) {
	if !isID32Wire(args.RequestID) || !isID32Wire(args.HandleID) || !isAddressWire(args.Address) {
		return nil, errors.New("arb: invalid account state request")
	}
	ident := arb.HandleIdentity{OwnerSession: args.RequestID, NodeBootID: api.svc.boot, Identity: args.RequestID}
	base, parent, release, err := api.svc.borrowBase(args.HandleID, ident)
	if err != nil {
		return nil, err
	}
	defer release()
	work := base.Copy()
	address := common.HexToAddress(args.Address)
	result := &GetAccountStateResult{Boot: api.svc.boot, HandleID: args.HandleID, ParentHash: parent.Hash().Hex(), Address: args.Address,
		Nonce: work.GetNonce(address), Balance: work.GetBalance(address).ToBig().String(), CodeHash: work.GetCodeHash(address).Hex()}
	return result, work.Error()
}
