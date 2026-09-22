package eth

import (
    _ "embed"
    "encoding/json"
    "math/big"
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

//go:embed arb/testdata/node-evm-fixture-v5.json
var flashRawFixtureV5JSON []byte

type flashLegV5 struct { AdapterId uint16; PoolOrManager common.Address; PoolKey [32]byte; TokenIn common.Address; TokenOut common.Address; MinAmountOut *big.Int; SqrtPriceLimitX96 *big.Int; AdapterData []byte }
type flashPlanV5 struct {
    OpportunityId [32]byte; TargetTxHash [32]byte; RequiredParentHash [32]byte; TargetBlockNumber uint64
    FinancingPool common.Address; BorrowToken common.Address; RepayToken common.Address; BorrowAmount *big.Int; RepayAmount *big.Int
    FinancingAdapterData []byte; MaxGasPriceWei *big.Int; BuilderPaymentRecipient common.Address; BuilderShareBps uint16
    ExpectedRouteStateDigest [32]byte; GasLimit *big.Int; TradeLegs []flashLegV5; SettlementLegs []flashLegV5
}

func TestExecutorV5ActualSignedMultiAssetBundle(t *testing.T) {
    var fixture struct { Init string `json:"fixture_init_code"`; Auth string `json:"auth_runtime_code"`; ABI json.RawMessage `json:"executor_abi"` }
    if err := json.Unmarshal(flashRawFixtureV5JSON, &fixture); err != nil { t.Fatal(err) }
    contractABI, err := abi.JSON(strings.NewReader(string(fixture.ABI))); if err != nil { t.Fatal(err) }
    key, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
    operator := crypto.PubkeyToAddress(key.PublicKey); recipient := common.HexToAddress("0xbeef"); fixtureAddress := crypto.CreateAddress(operator, 0)
    authAddress := common.HexToAddress("0x8b83F636C02FfBbE4811eF0d547A8518026d59e4")
    ether := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil); coin := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), ether) }; price := big.NewInt(50000000)
    gdb, db := rawdb.NewMemoryDatabase(), rawdb.NewMemoryDatabase()
    genesis := &core.Genesis{Config: params.TestChainConfig, GasLimit: 30000000, BaseFee: new(big.Int), Alloc: types.GenesisAlloc{operator: {Balance: coin(1000)}, authAddress: {Code: common.FromHex(fixture.Auth)}}}
    gb := genesis.MustCommit(gdb, triedb.NewDatabase(gdb, triedb.HashDefaults)); signer := types.LatestSigner(genesis.Config)
    data := append(common.FromHex(fixture.Init), common.LeftPadBytes(operator.Bytes(), 32)...); data = append(data, common.LeftPadBytes(recipient.Bytes(), 32)...)
    setup, err := types.SignTx(types.NewContractCreation(0, coin(210), 20000000, price, data), signer, key); if err != nil { t.Fatal(err) }
    chain, receipts := core.GenerateChain(genesis.Config, gb, ethash.NewFaker(), gdb, 1, func(i int, g *core.BlockGen) { g.AddTx(setup) })
    if receipts[0][0].Status != 1 { t.Fatal("fixture deployment reverted") }
    topic := crypto.Keccak256Hash([]byte("Ready(address,address,address,address,address,address,address)")); var a []common.Address
    for _, l := range receipts[0][0].Logs { if l.Address == fixtureAddress && len(l.Topics)==1 && l.Topics[0]==topic { for i:=0;i<7;i++ { a=append(a,common.BytesToAddress(l.Data[i*32:(i+1)*32])) } } }
    if len(a)!=7 { t.Fatal("fixture addresses unavailable") }; proxy, wbnb, usdt, doge, financing, trade, settlement := a[0],a[1],a[2],a[3],a[4],a[5],a[6]
    bc, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), core.DefaultConfig().WithStateScheme(rawdb.HashScheme)); if err != nil { t.Fatal(err) }; defer bc.Stop(); if _,err=bc.InsertChain(chain); err!=nil { t.Fatal(err) }
    parent:=chain[0].Header(); base,err:=bc.StateAt(parent.Root); if err!=nil {t.Fatal(err)}; x:=newTargetExecutor(bc,parent,base); env:=validEnvForExecutor(x)
    target,err:=types.SignTx(types.NewTransaction(1,fixtureAddress,new(big.Int),200000,price,crypto.Keccak256([]byte("makeProfitable()"))[:4]),signer,key);if err!=nil{t.Fatal(err)}
    fee:=append(common.LeftPadBytes(big.NewInt(997).Bytes(),32),common.LeftPadBytes(big.NewInt(1000).Bytes(),32)...); leg:=func(pool,in,out common.Address)flashLegV5{return flashLegV5{AdapterId:1,PoolOrManager:pool,TokenIn:in,TokenOut:out,MinAmountOut:new(big.Int),SqrtPriceLimitX96:new(big.Int),AdapterData:fee}}
    borrow:=coin(1); numerator:=new(big.Int).Mul(coin(100),borrow); numerator.Mul(numerator,big.NewInt(1000)); den:=new(big.Int).Mul(new(big.Int).Sub(coin(100),borrow),big.NewInt(997)); repay:=new(big.Int).Quo(numerator,den); if new(big.Int).Mod(numerator,den).Sign()!=0 {repay.Add(repay,big.NewInt(1))}
    plan:=flashPlanV5{OpportunityId:common.HexToHash("0x01"),TargetTxHash:target.Hash(),RequiredParentHash:parent.Hash(),TargetBlockNumber:parent.Number.Uint64()+1,FinancingPool:financing,BorrowToken:doge,RepayToken:usdt,BorrowAmount:borrow,RepayAmount:repay,FinancingAdapterData:fee,MaxGasPriceWei:price,BuilderPaymentRecipient:recipient,BuilderShareBps:8200,ExpectedRouteStateDigest:common.Hash{},GasLimit:big.NewInt(1400000),TradeLegs:[]flashLegV5{leg(trade,doge,usdt)},SettlementLegs:[]flashLegV5{leg(settlement,usdt,wbnb)}}
    calldata,err:=contractABI.Pack("executeMultiAsset",plan);if err!=nil{t.Fatal(err)};ours,err:=types.SignTx(types.NewTransaction(2,proxy,new(big.Int),1400000,price,calldata),signer,key);if err!=nil{t.Fatal(err)}
    txs:=[]*types.Transaction{target,ours}
    for i,tx:=range txs { raw,e:=tx.MarshalBinary();if e!=nil{t.Fatal(e)};txs[i],e=decodeRaw("0x"+common.Bytes2Hex(raw));if e!=nil{t.Fatal(e)};sender,e:=types.Sender(signer,txs[i]);if e!=nil||sender!=operator{t.Fatal("raw signer mismatch")} }
    mt:=measureTarget{measure:true,executor:proxy,baseToken:wbnb};if err=validateExecutorEnvelope(ours.To(),ours.Data(),ours.Gas(),mt);err!=nil{t.Fatal(err)}
    result,err:=x.ExecutePrefixWithEnv(txs,newReadBudget(5000000,0),env);if err!=nil{t.Fatal(err)};if !result.Completed{t.Fatalf("signed multi-asset bundle failed: %+v",result.Outcomes)}
    ledger,err:=executorLedgerV3(result.Outcomes[1].Receipt,calldata,mt);if err!=nil{t.Fatal(err)};if ledger["executor_revision"]!=executorRevisionMultiAsset{t.Fatal("wrong executor revision")}
    retained,_:=new(big.Int).SetString(ledger["retained_t3"].(string),10);if retained.Cmp(new(big.Int).Mul(new(big.Int).SetUint64(result.Outcomes[1].Receipt.GasUsed),price))<=0{t.Fatal("retained does not cover gas")}
    t0,_:=new(big.Int).SetString(ledger["retained_t0"].(string),10);t2,_:=new(big.Int).SetString(ledger["retained_t2"].(string),10)
    expectedPayment:=new(big.Int).Mul(new(big.Int).Sub(t2,t0),big.NewInt(int64(plan.BuilderShareBps)));expectedPayment.Div(expectedPayment,big.NewInt(10000))
    if ledger["retained_t0"]!="0" || ledger["owner_sweep"]!=ledger["retained_t3"] || result.PostState.GetBalance(recipient).ToBig().Cmp(expectedPayment)!=0 {t.Fatal("inventory, owner sweep or builder payment mismatch")}
    receipt:=result.Outcomes[1].Receipt;if receipt.EffectiveGasPrice==nil || receipt.EffectiveGasPrice.Cmp(price)!=0{t.Fatal("gas price mismatch")}
    early,_:=types.SignTx(types.NewTransaction(1,proxy,new(big.Int),1400000,price,calldata),signer,key)
    negative,err:=x.ExecuteTargetWithEnv(early,newReadBudget(5000000,0),env);if err!=nil || negative.Class.Status!=arb.StatusReverted{t.Fatal("pre-target negative control did not revert")}
    plan.BuilderShareBps=10000
    lowData,err:=contractABI.Pack("executeMultiAsset",plan);if err!=nil{t.Fatal(err)};low,_:=types.SignTx(types.NewTransaction(2,proxy,new(big.Int),1400000,price,lowData),signer,key)
    lowRaw,_:=low.MarshalBinary();low,err=decodeRaw("0x"+common.Bytes2Hex(lowRaw));if err!=nil{t.Fatal(err)}
    resolved,err:=x.resolveBlockEnv(env);if err!=nil{t.Fatal(err)};session:=x.newExecSession(newReadBudget(5000000,0),resolved)
    if session.applyOne(txs[0],0).Class.Status!=arb.StatusSuccess{t.Fatal("target failed")};rejected:=session.applyOne(low,1)
    if rejected.Class.Status!=arb.StatusReverted || !session.work.GetBalance(recipient).IsZero(){t.Fatal("gas floor failed to roll back builder payment")}
    t.Logf("multi-asset signed raw: gas=%d price=%s retained=%s sweep=%s",receipt.GasUsed,price,retained,ledger["owner_sweep"])
}
