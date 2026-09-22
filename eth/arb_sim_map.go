// Result mapping + the remaining two simulate methods (arb_simulateCandidate and
// arb_getPostPoolState), plus small adapter helpers shared by the sim layer. The
// mapping functions turn an executor result (TargetResult/PrefixResult/
// CandidateResult) into the wire JobOutcome, and on a real success register the
// isolated PostState as a CHILD handle so a later arb_getPostPoolState can read
// pools on it (design §206: job completion != handle destruction; the post handle
// has its own lifetime, kept alive by the child ref on the parent lease).

package eth

import (
	"errors"
	"math/big"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
	"github.com/ethereum/go-ethereum/log"
)

const (
	// defaultMaxReads bounds logical state reads per job (design §383 combined
	// limits; a fuller per-request override can be threaded later).
	defaultMaxReads = 200_000
	// postHandleTTL is how long a produced post handle stays borrowable before the
	// store closes it; the client releases sooner via releaseState.
	postHandleTTL = 30 * time.Second
)

func microsToDuration(us uint64) time.Duration { return time.Duration(us) * time.Microsecond }

// registerPostChild binds a successful run's isolated PostState as a child handle of
// the parent, returning the child id (the wire post_handle). The child keeps the
// parent's backend lease alive (§202) until released. Returns "" if post is nil
// (no quotable state on non-success, §256/§357) or the child cannot be created.
func (a *arbService) registerPostChild(parentHandle string, ident arb.HandleIdentity, post *state.StateDB, header *types.Header) string {
	a.handleMu.Lock()
	defer a.handleMu.Unlock()
	if post == nil || a.handles.Size() >= 4096 {
		return ""
	}
	parent, ok := a.handleStates.get(parentHandle)
	if !ok {
		return ""
	}
	ident = parent.identity
	childID, err := a.handles.CreateChild(parentHandle, ident, postHandleTTL)
	if err != nil {
		return ""
	}
	a.handleStates.put(childID, &pinnedState{identity: ident, header: header, root: header.Root, base: post})
	return childID
}

// meteringFrom builds the wire metering from a finished read budget and gas.
func meteringFrom(budget *arb.ReadBudget, gasUsed uint64) arb.Metering {
	var reads uint64
	if budget != nil {
		reads = budget.Reads()
	}
	return arb.Metering{StateReads: reads, GasUsed: gasUsed}
}

// mapTargetOutcome maps a single-target result. On success the post state becomes a
// child handle; otherwise post_handle is "".
func (api *ArbAPI) mapTargetOutcome(parentHandle string, ident arb.HandleIdentity, res *TargetResult, budget *arb.ReadBudget) *arb.JobOutcome {
	success := res.Class.Status == arb.StatusSuccess
	gas := uint64(0)
	if res.Class.ReceiptTrusted {
		gas = res.UsedGas
	}
	post := ""
	if success && res.PostState != nil {
		ps, _ := api.svc.handleStates.get(parentHandle)
		if ps != nil {
			post = api.svc.registerPostChild(parentHandle, ident, res.PostState, ps.header)
		}
	}
	return &arb.JobOutcome{
		Kind:       "target",
		Metering:   meteringFrom(budget, gas),
		Complete:   success,
		PostHandle: post,
		ErrCode:    nonSuccessCode(res.Class.Status),
	}
}

// mapPrefixOutcome maps a signed-bundle result. Post handle only on full completion.
func (api *ArbAPI) mapPrefixOutcome(parentHandle string, ident arb.HandleIdentity, res *PrefixResult, budget *arb.ReadBudget) *arb.JobOutcome {
	gas := uint64(0)
	for _, o := range res.Outcomes {
		if o.Class.ReceiptTrusted {
			gas += o.UsedGas
		}
	}
	post := ""
	if res.Completed && res.PostState != nil {
		ps, _ := api.svc.handleStates.get(parentHandle)
		if ps != nil {
			post = api.svc.registerPostChild(parentHandle, ident, res.PostState, ps.header)
		}
	}
	code := ""
	if !res.Completed {
		// The first non-success tx's status is the honest failure signal.
		if len(res.Outcomes) > 0 {
			code = nonSuccessCode(res.Outcomes[len(res.Outcomes)-1].Class.Status)
		} else {
			code = "empty_prefix"
		}
	}
	return &arb.JobOutcome{
		Kind:       "bundle",
		Metering:   meteringFrom(budget, gas),
		Complete:   res.Completed,
		PostHandle: post,
		ErrCode:    code,
	}
}

// mapCandidateOutcome maps a candidate result. Signed is always false (§8.3); the
// post handle appears only when prefix AND candidate all succeeded.
func (api *ArbAPI) mapCandidateOutcome(parentHandle string, ident arb.HandleIdentity, res *CandidateResult, budget *arb.ReadBudget) *arb.JobOutcome {
	gas := candidateExecutionGas(res)
	success := res.PrefixCompleted && res.Candidate != nil && res.Candidate.Class.Status == arb.StatusSuccess
	post := ""
	if success && res.PostState != nil {
		ps, _ := api.svc.handleStates.get(parentHandle)
		if ps != nil {
			post = api.svc.registerPostChild(parentHandle, ident, res.PostState, ps.header)
		}
	}
	code := ""
	if !success {
		if !res.PrefixCompleted && len(res.PrefixOutcomes) > 0 {
			code = nonSuccessCode(res.PrefixOutcomes[len(res.PrefixOutcomes)-1].Class.Status)
		} else if res.Candidate != nil {
			code = nonSuccessCode(res.Candidate.Class.Status)
		} else {
			code = "candidate_not_run"
		}
	}
	return &arb.JobOutcome{
		Kind:       "candidate",
		Metering:   meteringFrom(budget, gas),
		Complete:   success,
		PostHandle: post,
		ErrCode:    code,
	}
}

// Candidate metering is used to size OUR final transaction. Prefix gas belongs
// to the observed target and must never inflate or reject our execution envelope.
func candidateExecutionGas(res *CandidateResult) uint64 {
	if res.Candidate != nil && res.Candidate.Class.ReceiptTrusted {
		return res.Candidate.UsedGas
	}
	return 0
}

// logCandidateExecutionFailure emits the bounded, structured evidence needed to
// distinguish a genuine contract revert from an invalid candidate or an
// infrastructure failure. It deliberately stays in the adapter log rather than
// the frozen RPC schema: callers can continue to validate additionalProperties:false
// responses while operators still get selector/data/phase diagnostics.
func logCandidateExecutionFailure(args SimulateCandidateArgs, ours CandidateEnvelope, targetHash common.Hash, prefixLen int, res *CandidateResult) {
	if res == nil {
		return
	}
	revision := args.ExecutorRevision
	if revision == "" {
		if r, ok := executorRevisionForData(ours.Data); ok {
			revision = r
		}
	}
	executor := args.Executor
	if executor == "" {
		executor = addressHex(ours.To)
	}
	base := []any{
		"request_id", args.RequestID,
		"parent_handle", args.ParentHandle,
		"target_hash", targetHash.Hex(),
		"prefix_len", prefixLen,
		"executor", executor,
		"candidate_to", addressHex(ours.To),
		"candidate_calldata_digest", crypto.Keccak256Hash(ours.Data).Hex(),
		"executor_revision", revision,
	}
	logOne := func(phase string, out PrefixTxOutcome) {
		if out.Class.Status == arb.StatusSuccess {
			return
		}
		selector := "0x"
		if len(out.RevertData) >= 4 {
			selector = hexutil.Encode(out.RevertData[:4])
		}
		ctx := append(append([]any{}, base...),
			"phase", phase,
			"status", out.Class.Status.String(),
			"revert_selector", selector,
			"revert_data_len", out.RevertDataLen,
			"revert_data", hexutil.Encode(out.RevertData),
			"revert_data_truncated", out.RevertDataLen > len(out.RevertData),
			"gas_used", out.UsedGas,
		)
		// Reverts are rare and actionable, so keep them at the normal operator
		// level. Invalid/infra outcomes can be high-volume and remain debug-level.
		if out.Class.Status == arb.StatusReverted {
			log.Info("arb candidate execution failure", ctx...)
		} else {
			log.Debug("arb candidate execution failure", ctx...)
		}
	}
	if !res.PrefixCompleted {
		if n := len(res.PrefixOutcomes); n > 0 {
			logOne("prefix", res.PrefixOutcomes[n-1])
		}
		return
	}
	if res.Candidate != nil {
		logOne("candidate", *res.Candidate)
	}
}

func addressHex(addr *common.Address) string {
	if addr == nil {
		return ""
	}
	return addr.Hex()
}

// measureRetention fills out.RetainedT0/RetainedT2 with the §391 base-token balance of
// the beneficiary at the two isolated boundaries. Called ONLY on a successful candidate
// with a caller-named subject. It reads on private Copies of the T0/T2 states via the
// same budgeted read-only StaticCall path as pool reads, so it never mutates a quotable
// handle and cannot become a send permit. Best-effort and all-or-nothing: if either read
// fails/trips the budget, or a state snapshot is unexpectedly nil, BOTH fields stay ""
// (unmeasured) — an honest candidate result is never discarded for a missing gross seed,
// and a half-measured pair is never emitted (§389).
func (api *ArbAPI) measureRetention(x *targetExecutor, res *CandidateResult, mt measureTarget, budget *arb.ReadBudget, out *arb.JobOutcome) {
	t0Caller := x.PostStatePoolCaller(res.PrefixPostState, budget)
	t2Caller := x.PostStatePoolCaller(res.PostState, budget)
	if t0Caller == nil || t2Caller == nil {
		return // a snapshot was nil (should not happen on success) — stay unmeasured, never faked
	}
	t0, err := t0Caller.BalanceOf(mt.baseToken, mt.executor)
	if err != nil {
		return
	}
	t2, err := t2Caller.BalanceOf(mt.baseToken, mt.executor)
	if err != nil {
		return
	}
	// Both clean: emit the raw retention values. gross = T2 - T0 and the non-positive
	// rejection are the client's decision (§391), never inferred here.
	out.RetainedT0 = t0.String()
	out.RetainedT2 = t2.String()
}

// nonSuccessCode maps a non-success exec status to a wire error code; success -> "".
func nonSuccessCode(s arb.ExecStatus) string {
	if s == arb.StatusSuccess {
		return ""
	}
	return s.String()
}

// errMeasureTargetPairing: executor/base_token must be supplied TOGETHER or not at
// all, and each (when present) must be a valid address. Half a pair, or a malformed
// address, is a client mistake — rejected up front rather than silently unmeasured.
var errMeasureTargetPairing = errors.New("arb: executor and base_token must both be present valid addresses, or both absent")

// measureTarget names the §391 retention-measurement subject for a candidate sim.
type measureTarget struct {
	measure   bool           // false: caller asked for no retention measurement
	executor  common.Address // beneficiary whose base-token balance is read
	baseToken common.Address // standardized base token (e.g. WBNB) contract
}

// decodeMeasureTarget validates the optional executor/base_token pair. Both empty is
// the honest "don't measure" case (e.g. arb contract not deployed yet). Both valid is
// "measure". Anything else (one present, or a malformed address) is a client error —
// the node never guesses or hardcodes a beneficiary address (§389).
func decodeMeasureTarget(executor, baseToken string) (measureTarget, error) {
	if executor == "" && baseToken == "" {
		return measureTarget{measure: false}, nil
	}
	if !isAddressWire(executor) || !isAddressWire(baseToken) {
		return measureTarget{}, errMeasureTargetPairing
	}
	return measureTarget{
		measure:   true,
		executor:  common.HexToAddress(executor),
		baseToken: common.HexToAddress(baseToken),
	}, nil
}

// SimulateCandidate -> arb_simulateCandidate. Runs the signed target prefix then OUR
// unsigned envelope (no signature recovery, all other rules enforced, §237). Signed
// is always false in the result. When executor+base_token are supplied it also measures
// the §391 base-token retention at T0 (post-prefix, pre-candidate) and T2 (post-candidate).
func (api *ArbAPI) SimulateCandidate(args SimulateCandidateArgs) (*JobIDResult, error) {
	ident, err := api.commonSimValidate(args.RequestID, args.Stamp, args.ParentHandle, args.BudgetMicros)
	if err != nil {
		return nil, err
	}
	prefix, targetHash, err := decodeCandidatePrefix(args.TargetRaw)
	if err != nil {
		return nil, err
	}
	env, err := decodeUnsigned(args.UnsignedTx)
	if err != nil {
		return nil, err
	}
	mt, err := decodeMeasureTarget(args.Executor, args.BaseToken)
	if err != nil {
		return nil, err
	}
	v3 := args.SchemaVersion == "3"
	if args.SchemaVersion != "" && !v3 {
		return nil, errUnsupportedSchema
	}
	if v3 {
		if args.ExecutorRevision != executorRevisionV3 && args.ExecutorRevision != executorRevisionMultiAsset {
			return nil, errUnsupportedSchema
		}
		if err := validateExecutorEnvelope(env.To, env.Data, env.GasLimit, mt); err != nil {
			return nil, err
		}
	}
	// The measurement subject is part of request identity (§295): the same target+envelope
	// asked WITH vs WITHOUT retention measurement, or against a different beneficiary/base
	// token, are genuinely different requests and must not alias one job's cached result.
	dig := arb.NewDigest("arb.req.simulateCandidate.v1").
		FieldBytes([]byte(args.ParentHandle)).
		FieldBytes([]byte(args.Stamp)).
		FieldBytes(targetHash.Bytes()).
		FieldBytes(env.From.Bytes()).
		FieldU64(env.Nonce).
		FieldU64(args.BudgetMicros)
	if mt.measure {
		dig.FieldBytes(mt.executor.Bytes()).FieldBytes(mt.baseToken.Bytes())
	}
	digest := hex32Str(dig.Finalize())

	run := func(budget *arb.ReadBudget) (*arb.JobOutcome, bool) {
		base, header, release, berr := api.svc.borrowBase(args.ParentHandle, ident)
		if berr != nil {
			return &arb.JobOutcome{Kind: "candidate", ErrCode: berr.Error()}, false
		}
		defer release()
		x := newTargetExecutor(api.svc.eth.blockchain, header, base)
		res, xerr := x.RunCandidateWithEnv(prefix, env, PurposeDiagnostic, budget, args.BlockEnv)
		if xerr != nil {
			return &arb.JobOutcome{Kind: "candidate", ErrCode: xerr.Error()}, false
		}
		logCandidateExecutionFailure(args, env, targetHash, len(prefix), res)
		out := api.mapCandidateOutcome(args.ParentHandle, ident, res, budget)
		// §391 retention: measure ONLY when the caller named a subject AND the candidate
		// actually succeeded (out.Complete). T0 reads the isolated post-prefix snapshot,
		// T2 the post-candidate state — same budgeted read-only path as pool reads. A read
		// failure leaves both fields "" (unmeasured) without failing the job: an honest
		// candidate result must not be discarded because the optional gross seed couldn't
		// be read (§389 — the client then treats gross as unmeasured, never fabricated).
		if v3 && out.Complete {
			payload, err := executorLedgerV3(res.Candidate.Receipt, env.Data, mt)
			if err != nil {
				out.Complete = false
				out.ErrCode = err.Error()
			} else {
				out.ExecutorPayload = payload
			}
		} else if mt.measure && out.Complete {
			api.measureRetention(x, res, mt, budget, out)
		}
		return out, out.Complete
	}

	jobID, err := api.submitAndEnqueue(arb.NamespaceSimulateCandidate, args.RequestID, args.Stamp, digest, args.BudgetMicros, run)
	if err != nil {
		return nil, err
	}
	return &JobIDResult{JobID: jobID}, nil
}

// decodeCandidatePrefix distinguishes the two supported candidate sources.
// "0x" is an explicit canonical-head state source and therefore has no signed
// prefix and a zero target hash. Any other value must decode as one real signed
// target; a missing field ("") remains invalid and cannot silently become direct.
func decodeCandidatePrefix(raw string) ([]*types.Transaction, common.Hash, error) {
	if raw == "0x" {
		return nil, common.Hash{}, nil
	}
	target, err := decodeRaw(raw)
	if err != nil {
		return nil, common.Hash{}, err
	}
	return []*types.Transaction{target}, target.Hash(), nil
}

// decodeUnsigned turns the wire unsigned envelope into a CandidateEnvelope. from must
// be an explicit non-zero sender (§7.1); amounts decode from decimal-or-hex strings.
func decodeUnsigned(u UnsignedEnvelope) (CandidateEnvelope, error) {
	if !isAddressWire(u.From) {
		return CandidateEnvelope{}, errBadSender
	}
	from := common.HexToAddress(u.From)
	if from == (common.Address{}) {
		return CandidateEnvelope{}, errors.New("arb: unsigned_tx.from must be explicit non-zero")
	}
	value, err := decodeBig(u.Value)
	if err != nil {
		return CandidateEnvelope{}, errors.New("arb: unsigned_tx.value invalid")
	}
	feeCap, err := decodeBig(u.GasFeeCap)
	if err != nil {
		return CandidateEnvelope{}, errors.New("arb: unsigned_tx.gas_fee_cap invalid")
	}
	tipCap, err := decodeBig(u.GasTipCap)
	if err != nil {
		return CandidateEnvelope{}, errors.New("arb: unsigned_tx.gas_tip_cap invalid")
	}
	var to *common.Address
	if u.To != "" {
		if !isAddressWire(u.To) {
			return CandidateEnvelope{}, errors.New("arb: unsigned_tx.to invalid")
		}
		t := common.HexToAddress(u.To)
		to = &t
	}
	var data []byte
	if u.Data != "" {
		b, derr := hexutil.Decode(u.Data)
		if derr != nil {
			return CandidateEnvelope{}, errors.New("arb: unsigned_tx.data invalid hex")
		}
		data = b
	}
	return CandidateEnvelope{
		From:      from,
		To:        to,
		Nonce:     u.Nonce,
		Value:     value,
		GasLimit:  u.GasLimit,
		GasFeeCap: feeCap,
		GasTipCap: tipCap,
		Data:      data,
	}, nil
}

// decodeBig parses a decimal or 0x-hex string into a non-negative big.Int. Empty
// string is treated as zero.
func decodeBig(s string) (*big.Int, error) {
	if s == "" {
		return big.NewInt(0), nil
	}
	if len(s) >= 2 && s[0] == '0' && s[1] == 'x' {
		v, err := hexutil.DecodeBig(s)
		if err != nil {
			return nil, err
		}
		return v, nil
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || v.Sign() < 0 {
		return nil, errors.New("bad integer")
	}
	return v, nil
}

// GetPostPoolStateArgs is arb_getPostPoolState params (wire contract v2). It reads the
// named pools on a previously-produced post handle (the child state from a successful
// simulate). v2 replaces v1's pool_locators (address-only) with structured pool_reads:
// the node holds NO pool registry, so the client must declare each pool's kind and
// (for v2) its token0/token1 — pool identity authority lives in the Rust PoolMeta, the
// node is a dumb executor that reads what it is told.
type GetPostPoolStateArgs struct {
	RequestID      string         `json:"request_id"`
	Stamp          string         `json:"stamp"`
	PostHandle     string         `json:"post_handle"`
	RegistryDigest string         `json:"registry_digest"`
	PoolReads      []PoolReadSpec `json:"pool_reads"`
	MissingParts   []any          `json:"missing_parts"`
	BudgetMicros   uint64         `json:"budget_us"`
}

// PoolReadSpec is one structured pool read (v2). Kind selects the read path: "v2" needs
// token0/token1 (getReserves + balanceOf on both tokens); "v3" needs only the locator
// (slot0 + liquidity). The node never guesses a kind or token identity (§389).
type PoolReadSpec struct {
	Locator     string `json:"locator"`
	Kind        string `json:"kind"`
	Token0      string `json:"token0,omitempty"`
	Token1      string `json:"token1,omitempty"`
	Manager     string `json:"manager,omitempty"`
	PoolKey     string `json:"pool_key,omitempty"`
	Hook        string `json:"hook,omitempty"`
	TickSpacing int32  `json:"tick_spacing,omitempty"`
}

// errBadPoolRead: a pool_reads entry is malformed — bad locator, unknown kind, or a v2
// read missing/malforming its required token0/token1. Rejected up front; the node never
// substitutes a guessed identity.
var errBadPoolRead = errors.New("arb: pool_reads entry malformed (locator/kind/token invalid)")

// validPoolRead enforces the §389 no-guess rule on one read spec: the locator must be a
// valid address; kind must be "v2" or "v3"; a v2 read MUST carry valid token0/token1
// (the node has no registry to look them up); a v3 read must NOT carry tokens (they are
// meaningless for slot0/liquidity and their presence signals a client encoding bug).
func validPoolRead(pr *PoolReadSpec) bool {
	if !isAddressWire(pr.Locator) {
		return false
	}
	switch pr.Kind {
	case "v2":
		return isAddressWire(pr.Token0) && isAddressWire(pr.Token1)
	case "v3":
		return pr.Token0 == "" && pr.Token1 == "" && pr.TickSpacing >= 0 && pr.TickSpacing <= 16383
	case "infinity_cl":
		return isAddressWire(pr.Manager) && isHash32Wire(pr.PoolKey) && isAddressWire(pr.Hook) &&
			isAddressWire(pr.Token0) && isAddressWire(pr.Token1) && pr.Locator == pr.Manager && pr.TickSpacing > 0 && pr.TickSpacing <= 16383
	default:
		return false
	}
}

// GetPostPoolState -> arb_getPostPoolState. Like the simulate methods it returns a
// job_id; the read runs on the post handle's state via the PostStatePoolCaller
// bridge, and the job's result carries the read snapshots (v1: metering + a
// completeness flag; the full snapshot payload rides the result once the wire result
// schema for pool batches is finalized).
func (api *ArbAPI) GetPostPoolState(args GetPostPoolStateArgs) (*JobIDResult, error) {
	if !isID32Wire(args.RequestID) {
		return nil, errBadRequestID
	}
	if !isHash32Wire(args.Stamp) {
		return nil, errBadStampWire
	}
	if !isID32Wire(args.PostHandle) {
		return nil, errBadHandleWire
	}
	if args.BudgetMicros == 0 {
		return nil, errZeroBudget
	}
	// Validate every pool read spec up front (§389: reject malformed, never guess).
	for i := range args.PoolReads {
		if !validPoolRead(&args.PoolReads[i]) {
			return nil, errBadPoolRead
		}
	}
	ident := arb.HandleIdentity{OwnerSession: args.RequestID, NodeBootID: api.svc.boot, Identity: args.RequestID}
	// v2 digest folds the structured read specs (kind + tokens), not bare locators, so
	// a v2 read is content-addressed distinctly from a v1 address-only read.
	d := arb.NewDigest("arb.req.getPostPoolState.v2").
		FieldBytes([]byte(args.PostHandle)).
		FieldBytes([]byte(args.Stamp)).
		FieldBytes([]byte(args.RegistryDigest)).
		FieldU64(args.BudgetMicros)
	for i := range args.PoolReads {
		pr := &args.PoolReads[i]
		d.FieldBytes([]byte(pr.Locator)).FieldBytes([]byte(pr.Kind)).
			FieldBytes([]byte(pr.Token0)).FieldBytes([]byte(pr.Token1))
		if pr.Kind == "v3" && pr.TickSpacing > 0 {
			d.FieldBytes([]byte(strconv.FormatInt(int64(pr.TickSpacing), 10)))
		}
		if pr.Kind == "infinity_cl" {
			d.FieldBytes([]byte(pr.Manager)).FieldBytes([]byte(pr.PoolKey)).FieldBytes([]byte(pr.Hook)).FieldBytes([]byte(strconv.FormatInt(int64(pr.TickSpacing), 10)))
		}
	}
	digest := hex32Str(d.Finalize())

	run := func(budget *arb.ReadBudget) (*arb.JobOutcome, bool) {
		base, header, release, berr := api.svc.borrowBase(args.PostHandle, ident)
		if berr != nil {
			return &arb.JobOutcome{Kind: "post_pool", ErrCode: berr.Error()}, false
		}
		defer release()
		// Read pools on the post state via the same budgeted+StaticCall path used at
		// head (newPoolCaller over the borrowed post base). Each worker owns a
		// private state copy and budget, so the batch is split across a bounded
		// number of readers instead of serializing every V3 deep read (5 bitmap
		// words + up to 32 tick records each). A read failure still aborts the
		// WHOLE job with an error -- never a partial or fabricated snapshot.
		n := len(args.PoolReads)
		snaps := make([]arb.PoolSnapshot, n)
		workers := postPoolReadWorkers
		if workers > n {
			workers = n
		}
		if workers < 1 {
			workers = 1
		}
		callers := make([]*poolCaller, workers)
		budgets := make([]*arb.ReadBudget, workers)
		for w := 0; w < workers; w++ {
			budgets[w] = budget.Fork()
			callers[w] = api.svc.eth.newPoolCaller(base, header, budgets[w])
		}
		errs := make([]error, workers)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			lo := w * n / workers
			hi := (w + 1) * n / workers
			wg.Add(1)
			go func(w, lo, hi int) {
				defer wg.Done()
				for i := lo; i < hi; i++ {
					s, rerr := readPostPool(callers[w], &args.PoolReads[i])
					if rerr != nil {
						errs[w] = rerr
						return
					}
					snaps[i] = s
				}
			}(w, lo, hi)
		}
		wg.Wait()
		for w := 0; w < workers; w++ {
			budget.Absorb(budgets[w])
			if errs[w] != nil {
				return &arb.JobOutcome{Kind: "post_pool", ErrCode: errs[w].Error()}, false
			}
		}
		return &arb.JobOutcome{
			Kind:      "post_pool",
			Metering:  meteringFrom(budget, 0),
			Complete:  len(snaps) == len(args.PoolReads),
			Snapshots: snaps,
		}, true
	}

	jobID, err := api.submitAndEnqueue(arb.NamespaceGetPostPoolState, args.RequestID, args.Stamp, digest, args.BudgetMicros, run)
	if err != nil {
		return nil, err
	}
	return &JobIDResult{JobID: jobID}, nil
}

// postPoolReadWorkers bounds how many pool reads run concurrently inside one
// arb_getPostPoolState job. The pending path reads ~40 pools (10-12 of them V3
// deep reads) per prepare, which dominated candidate build latency when serial.
const postPoolReadWorkers = 4

// readPostPool reads one declared pool on a caller's private state copy. It
// mirrors the serial path exactly; only the execution order changes.
func readPostPool(caller *poolCaller, pr *PoolReadSpec) (arb.PoolSnapshot, error) {
	pool := common.HexToAddress(pr.Locator)
	switch pr.Kind {
	case "infinity_cl":
		manager := common.HexToAddress(pr.Manager)
		poolID := common.HexToHash(pr.PoolKey)
		s, rerr := caller.ReadInfinityCLWithSpacing(manager, poolID, int64(pr.TickSpacing))
		if rerr != nil {
			return arb.PoolSnapshot{}, rerr
		}
		words := make([]arb.InfinityBitmapWord, 0, len(s.BitmapWords))
		for _, w := range s.BitmapWords {
			words = append(words, arb.InfinityBitmapWord{Index: strconv.FormatInt(int64(w.Index), 10), Value: w.Value.String()})
		}
		ticks := make([]arb.InfinityTick, 0, len(s.InitializedTicks))
		for _, t := range s.InitializedTicks {
			ticks = append(ticks, arb.InfinityTick{Index: strconv.FormatInt(int64(t.Index), 10), LiquidityGross: t.LiquidityGross.String(), LiquidityNet: t.LiquidityNet.String()})
		}
		feeNum, feeDen := "", ""
		if s.EffectiveFeeResolved {
			feeNum = strconv.FormatUint(uint64(s.EffectiveFeeNum), 10)
			feeDen = strconv.FormatUint(uint64(s.EffectiveFeeDen), 10)
		} else {
			// A dynamic PoolKey's slot0.lpFee is not an amount-specific quote.
			// Keep the pair absent on the wire and leave an explicit diagnostic
			// in the node log instead of emitting "0".
			log.Debug("arb infinity effective fee unresolved", "manager", s.Manager.Hex(), "pool", s.PoolKey.Hex(), "status", s.EffectiveFeeStatus)
		}
		return arb.PoolSnapshot{Locator: pr.Locator, Kind: "infinity_cl", Manager: s.Manager.Hex(), PoolKey: s.PoolKey.Hex(), Hook: s.Hook.Hex(),
			SqrtPriceX96: s.SqrtPriceX96.String(), Tick: strconv.FormatInt(int64(s.Tick), 10), Liquidity: s.Liquidity.String(), BitmapWords: words,
			InitializedTicks: ticks, CoverageMinTick: strconv.FormatInt(int64(s.CoverageMinTick), 10), CoverageMaxTick: strconv.FormatInt(int64(s.CoverageMaxTick), 10),
			EffectiveFeeNum: feeNum, EffectiveFeeDen: feeDen, EffectiveFeeResolved: s.EffectiveFeeResolved, EffectiveFeeStatus: s.EffectiveFeeStatus}, nil
	case "v3":
		if pr.TickSpacing > 0 {
			s, rerr := caller.ReadV3Full(pool, pr.TickSpacing)
			if rerr != nil {
				return arb.PoolSnapshot{}, rerr
			}
			s.Locator = pr.Locator
			return s, nil
		}
		s, rerr := caller.ReadV3Head(pool)
		if rerr != nil {
			return arb.PoolSnapshot{}, rerr
		}
		return arb.PoolSnapshot{Locator: pr.Locator, Kind: "v3", SqrtPriceX96: s.SqrtPriceX96.String(), Tick: strconv.FormatInt(int64(s.Tick), 10), Liquidity: s.Liquidity.String()}, nil
	default: // "v2" (validPoolRead guaranteed kind is v2 or v3, and v2 has tokens)
		s, rerr := caller.ReadV2(pool, common.HexToAddress(pr.Token0), common.HexToAddress(pr.Token1))
		if rerr != nil {
			return arb.PoolSnapshot{}, rerr
		}
		return arb.PoolSnapshot{
			Locator:  pr.Locator,
			Kind:     "v2",
			Reserve0: s.Reserve0.String(),
			Reserve1: s.Reserve1.String(),
			Balance0: s.Balance0.String(),
			Balance1: s.Balance1.String(),
		}, nil
	}
}
