package eth

import (
	_ "embed"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/arb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

//go:embed arb/testdata/node-evm-fixture-v4.json
var flashRawFixtureV4JSON []byte

type flashLegV4 struct {
	AdapterId         uint16
	PoolOrManager     common.Address
	PoolKey           [32]byte
	TokenIn           common.Address
	TokenOut          common.Address
	MinAmountOut      *big.Int
	SqrtPriceLimitX96 *big.Int
	AdapterData       []byte
}
type flashPlanV4 struct {
	OpportunityId            [32]byte
	TargetTxHash             [32]byte
	RequiredParentHash       [32]byte
	TargetBlockNumber        uint64
	BaseToken                common.Address
	AmountIn                 *big.Int
	MinRetainedBeforeGas     *big.Int
	MaxGasPriceWei           *big.Int
	BuilderPaymentRecipient  common.Address
	BuilderPaymentWei        *big.Int
	ExpectedRouteStateDigest [32]byte
	GasLimit                 *big.Int
	Legs                     []flashLegV4
}

func TestExecutorV4ActualSignedFlashBundle(t *testing.T) {
	var fixture struct {
		Init string          `json:"fixture_init_code"`
		Auth string          `json:"auth_runtime_code"`
		ABI  json.RawMessage `json:"executor_abi"`
	}
	if err := json.Unmarshal(flashRawFixtureV4JSON, &fixture); err != nil {
		t.Fatal(err)
	}
	contractABI, err := abi.JSON(strings.NewReader(string(fixture.ABI)))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	operator := crypto.PubkeyToAddress(key.PublicKey)
	recipient := common.HexToAddress("0xbeef")
	fixtureAddress := crypto.CreateAddress(operator, 0)
	authAddress := common.HexToAddress("0x8b83F636C02FfBbE4811eF0d547A8518026d59e4")
	ether := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	coin := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), ether) }
	price := big.NewInt(50000000)
	gdb, db := rawdb.NewMemoryDatabase(), rawdb.NewMemoryDatabase()
	genesis := &core.Genesis{Config: params.TestChainConfig, GasLimit: 30000000, BaseFee: new(big.Int), Alloc: types.GenesisAlloc{
		operator: {Balance: coin(1000)}, authAddress: {Code: common.FromHex(fixture.Auth), Balance: new(big.Int)},
	}}
	gb := genesis.MustCommit(gdb, triedb.NewDatabase(gdb, triedb.HashDefaults))
	signer := types.LatestSigner(genesis.Config)
	data := common.FromHex(fixture.Init)
	data = append(data, common.LeftPadBytes(operator.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(recipient.Bytes(), 32)...)
	setup, err := types.SignTx(types.NewContractCreation(0, coin(210), 20000000, price, data), signer, key)
	if err != nil {
		t.Fatal(err)
	}
	chain, receipts := core.GenerateChain(genesis.Config, gb, ethash.NewFaker(), gdb, 1, func(i int, g *core.BlockGen) { g.AddTx(setup) })
	setupReceipt := receipts[0][0]
	if setupReceipt.Status != 1 {
		t.Fatal("fixture deployment reverted")
	}
	topic := crypto.Keccak256Hash([]byte("Ready(address,address,address,address,address)"))
	var addresses []common.Address
	for _, log := range setupReceipt.Logs {
		if log.Address == fixtureAddress && len(log.Topics) == 1 && log.Topics[0] == topic {
			for i := 0; i < 5; i++ {
				addresses = append(addresses, common.BytesToAddress(log.Data[i*32:(i+1)*32]))
			}
		}
	}
	if len(addresses) != 5 {
		t.Fatal("fixture addresses unavailable")
	}
	proxy, wbnb, token, p1, p2 := addresses[0], addresses[1], addresses[2], addresses[3], addresses[4]
	bc, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Stop()
	if _, err = bc.InsertChain(chain); err != nil {
		t.Fatal(err)
	}
	parent := chain[0].Header()
	base, err := bc.StateAt(parent.Root)
	if err != nil {
		t.Fatal(err)
	}
	x := newTargetExecutor(bc, parent, base)
	env := validEnvForExecutor(x)
	target, err := types.SignTx(types.NewTransaction(1, fixtureAddress, new(big.Int), 200000, price, crypto.Keccak256([]byte("makeProfitable()"))[:4]), signer, key)
	if err != nil {
		t.Fatal(err)
	}
	fees := append(common.LeftPadBytes(big.NewInt(997).Bytes(), 32), common.LeftPadBytes(big.NewInt(1000).Bytes(), 32)...)
	leg := func(pool, in, out common.Address) flashLegV4 {
		return flashLegV4{AdapterId: 1, PoolOrManager: pool, TokenIn: in, TokenOut: out, MinAmountOut: new(big.Int), SqrtPriceLimitX96: new(big.Int), AdapterData: fees}
	}
	plan := flashPlanV4{OpportunityId: common.HexToHash("0x01"), TargetTxHash: target.Hash(), RequiredParentHash: parent.Hash(), TargetBlockNumber: parent.Number.Uint64() + 1, BaseToken: wbnb, AmountIn: coin(1), MinRetainedBeforeGas: big.NewInt(10000000000000000), MaxGasPriceWei: price, BuilderPaymentRecipient: recipient, BuilderPaymentWei: big.NewInt(10000000000000000), GasLimit: big.NewInt(1400000), Legs: []flashLegV4{leg(p1, wbnb, token), leg(p2, token, wbnb)}}
	calldata, err := contractABI.Pack("execute", plan)
	if err != nil {
		t.Fatal(err)
	}
	ours, err := types.SignTx(types.NewTransaction(2, proxy, new(big.Int), 1400000, price, calldata), signer, key)
	if err != nil {
		t.Fatal(err)
	}
	// Marshal then decode exactly as the RPC does. Never replace signatures,
	// nonces, fee fields or calldata with an unsigned message for final testing.
	raws := []string{}
	txs := []*types.Transaction{}
	for _, tx := range []*types.Transaction{target, ours} {
		raw, _ := tx.MarshalBinary()
		raws = append(raws, "0x"+common.Bytes2Hex(raw))
		decoded, err := decodeRaw(raws[len(raws)-1])
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, decoded)
	}
	mt := measureTarget{measure: true, executor: proxy, baseToken: wbnb}
	if err = validateExecutorEnvelope(ours.To(), ours.Data(), ours.Gas(), mt); err != nil {
		t.Fatal(err)
	}
	result, err := x.ExecutePrefixWithEnv(txs, newReadBudget(5000000, 0), env)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed || len(result.Outcomes) != 2 {
		t.Fatalf("actual signed flash bundle failed: %+v", result.Outcomes)
	}
	receipt := result.Outcomes[1].Receipt
	ledger, err := executorLedgerV3(receipt, calldata, mt)
	if err != nil {
		t.Fatal(err)
	}
	if ledger["retained_t0"] != "0" {
		t.Fatal("executor unexpectedly needed inventory")
	}
	retained, _ := new(big.Int).SetString(ledger["retained_t3"].(string), 10)
	gasCost := new(big.Int).Mul(new(big.Int).SetUint64(receipt.GasUsed), price)
	if retained.Cmp(gasCost) <= 0 {
		t.Fatal("successful bundle did not cover actual receipt gas")
	}
	if result.PostState.GetBalance(recipient).ToBig().Cmp(plan.BuilderPaymentWei) != 0 {
		t.Fatal("builder payment mismatch")
	}
	if receipt.EffectiveGasPrice == nil || receipt.EffectiveGasPrice.Cmp(price) != 0 {
		t.Fatal("raw gas price changed")
	}
	// Sending the backrun first fails nonce validation, proving the prefix order
	// matters. Also give it the pre-target nonce: the original pool price loses.
	reversed, err := x.ExecutePrefixWithEnv([]*types.Transaction{txs[1], txs[0]}, newReadBudget(5000000, 0), env)
	if err != nil || reversed.Completed {
		t.Fatal("reversed signed raw order accepted")
	}
	early, _ := types.SignTx(types.NewTransaction(1, proxy, new(big.Int), 1400000, price, calldata), signer, key)
	noTarget, err := x.ExecuteTargetWithEnv(early, newReadBudget(5000000, 0), env)
	if err != nil || noTarget.Class.Status != arb.StatusReverted {
		t.Fatalf("pre-target route must revert: %+v %v", noTarget, err)
	}
	// Positive retained value below gas cost must still revert the signed raw,
	// including the already-attempted builder transfer.
	plan.MinRetainedBeforeGas = new(big.Int)
	plan.BuilderPaymentWei = new(big.Int).Sub(new(big.Int).Add(retained, plan.BuilderPaymentWei), big.NewInt(1000000000000))
	lowData, err := contractABI.Pack("execute", plan)
	if err != nil {
		t.Fatal(err)
	}
	lowProfit, _ := types.SignTx(types.NewTransaction(2, proxy, new(big.Int), 1400000, price, lowData), signer, key)
	lowRaw, _ := lowProfit.MarshalBinary()
	lowProfit, err = decodeRaw("0x" + common.Bytes2Hex(lowRaw))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := x.resolveBlockEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	session := x.newExecSession(newReadBudget(5000000, 0), resolved)
	if session.applyOne(target, 0).Class.Status != arb.StatusSuccess {
		t.Fatal("target failed")
	}
	rejected := session.applyOne(lowProfit, 1)
	if rejected.Class.Status != arb.StatusReverted || !session.work.GetBalance(recipient).IsZero() {
		t.Fatal("gas floor did not roll back raw and builder payment")
	}
	if _, err = executorLedgerV3(rejected.Receipt, lowData, mt); err == nil {
		t.Fatal("reverted raw produced accepted ledger")
	}
	t.Logf("signed flash evidence: target=%s ours=%s gas=%d price=%s retained=%s block=%s", target.Hash(), ours.Hash(), receipt.GasUsed, price, retained, strconv.FormatUint(plan.TargetBlockNumber, 10))
}
