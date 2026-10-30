package eth

// The feed supplies post-target storage, not a signed target transaction.
// Apply it only to the private state returned by borrowBase. Unmentioned slots,
// account balances, code and nonces retain the pinned parent's values.
import (
	"errors"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/eth/arb"
)

type AuctionState struct {
	ID            string                       `json:"id"`
	ContentDigest string                       `json:"content_digest"`
	States        map[string]map[string]string `json:"states"`
}

func (a *AuctionState) digest() (common.Hash, error) {
	bad := errors.New("arb: invalid auction storage diff")
	if a == nil || !isID32Wire(a.ID) || a.ID == (common.Hash{}).Hex() || !isID32Wire(a.ContentDigest) || len(a.States) == 0 || len(a.States) > 1024 {
		return common.Hash{}, bad
	}
	d := arb.NewDigest("arb.auction.overlay.v1").FieldBytes(common.HexToHash(a.ID).Bytes()).FieldBytes(common.HexToHash(a.ContentDigest).Bytes()).FieldU32(uint32(len(a.States)))
	addresses := make([]string, 0, len(a.States))
	for address := range a.States {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	count := 0
	for _, address := range addresses {
		if !isAddressWire(address) {
			return common.Hash{}, bad
		}
		slots := a.States[address]
		count += len(slots)
		if count > 16384 {
			return common.Hash{}, bad
		}
		d.FieldBytes(common.HexToAddress(address).Bytes()).FieldU32(uint32(len(slots)))
		keys := make([]string, 0, len(slots))
		for key := range slots {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !isID32Wire(key) || !isID32Wire(slots[key]) {
				return common.Hash{}, bad
			}
			d.FieldBytes(common.HexToHash(key).Bytes()).FieldBytes(common.HexToHash(slots[key]).Bytes())
		}
	}
	return common.Hash(d.Finalize()), nil
}

func (a *AuctionState) apply(work *state.StateDB) {
	for address, slots := range a.States {
		for key, value := range slots {
			work.SetState(common.HexToAddress(address), common.HexToHash(key), common.HexToHash(value))
		}
	}
	work.Finalise(true)
}

// Bind a distinct method/result and the complete overlay to the existing
// request digest. A canonical state result cannot satisfy this identity.
func auctionRequestDigest(inner string, overlay common.Hash) string {
	return hex32Str(arb.NewDigest("arb.req.auction.v1").FieldBytes(common.HexToHash(inner).Bytes()).FieldBytes(overlay.Bytes()).Finalize())
}

type PrepareAuctionArgs struct {
	RequestID    string       `json:"request_id"`
	Stamp        string       `json:"stamp"`
	ParentHandle string       `json:"parent_handle"`
	Auction      AuctionState `json:"auction"`
	BudgetMicros uint64       `json:"budget_us"`
}

func (api *ArbAPI) PrepareAuction(args PrepareAuctionArgs) (*JobIDResult, error) {
	ident, err := api.commonSimValidate(args.RequestID, args.Stamp, args.ParentHandle, args.BudgetMicros)
	if err != nil {
		return nil, err
	}
	diff, err := args.Auction.digest()
	if err != nil {
		return nil, err
	}
	digest := hex32Str(arb.NewDigest("arb.req.prepareAuction.v1").FieldBytes([]byte(args.ParentHandle)).FieldBytes([]byte(args.Stamp)).FieldBytes(diff.Bytes()).Finalize())
	run := func(budget *arb.ReadBudget) (*arb.JobOutcome, bool) {
		base, header, release, err := api.svc.borrowBase(args.ParentHandle, ident)
		if err != nil {
			return &arb.JobOutcome{Kind: "auction_overlay", ErrCode: err.Error()}, false
		}
		defer release()
		args.Auction.apply(base)
		if err := base.Error(); err != nil {
			return &arb.JobOutcome{Kind: "auction_overlay", ErrCode: err.Error()}, false
		}
		post := api.svc.registerPostChild(args.ParentHandle, ident, base, header)
		return &arb.JobOutcome{Kind: "auction_overlay", Complete: post != "", PostHandle: post, ExecutorPayload: map[string]interface{}{"overlay_digest": diff.Hex(), "auction_id": args.Auction.ID}}, post != ""
	}
	id, err := api.submitAndEnqueue(arb.JobNamespace("arb_prepareAuction"), args.RequestID, args.Stamp, digest, args.BudgetMicros, run)
	if err != nil {
		return nil, err
	}
	return &JobIDResult{JobID: id}, nil
}

type SimulateAuctionCandidateArgs struct {
	SimulateCandidateArgs
	Auction AuctionState `json:"auction"`
}

func (api *ArbAPI) SimulateAuctionCandidate(args SimulateAuctionCandidateArgs) (*JobIDResult, error) {
	if args.TargetRaw != "" && args.TargetRaw != "0x" {
		return nil, errors.New("arb: auction cannot include target raw")
	}
	return api.simulateCandidate(args.SimulateCandidateArgs, &args.Auction)
}

type SimulateAuctionRawArgs struct {
	SimulateStateRawArgs
	Auction AuctionState `json:"auction"`
}

func (api *ArbAPI) SimulateAuctionRaw(args SimulateAuctionRawArgs) (*JobIDResult, error) {
	return api.simulateStateRaw(args.SimulateStateRawArgs, &args.Auction)
}
