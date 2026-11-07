package eth

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

func TestPendingFlapApprovalSharesStateWithSale(t *testing.T) {
	cfg := params.TestChainConfig
	header := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1), GasLimit: 10_000_000}
	signer := types.MakeSigner(cfg, header.Number, header.Time)
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := crypto.PubkeyToAddress(key.PublicKey)
	base, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	base.AddBalance(sender, uint256.NewInt(1_000_000_000), tracing.BalanceChangeUnspecified)
	token := common.HexToAddress("0x12347777")

	// Approve stores a flag in slot zero. An empty call reads the flag.
	base.SetCode(token, common.FromHex("0x3660441460125760005460005260206000f35b600160005500"), tracing.CodeChangeUnspecified)
	// The Portal reads the token flag before emitting Sell with that token.
	portalCode := append(common.FromHex("0x602060006000600073"), token[:]...)
	portalCode = append(portalCode, common.FromHex("0x5afa50600051602b5760006000fd5b73")...)
	portalCode = append(portalCode, token[:]...)
	portalCode = append(portalCode, common.FromHex("0x6020527f")...)
	portalCode = append(portalCode, flapPortalSell[:]...)
	portalCode = append(portalCode, common.FromHex("0x60e06000a100")...)
	base.SetCode(flapPortal, portalCode, tracing.CodeChangeUnspecified)

	signed := func(nonce uint64, to common.Address, data []byte) *types.Transaction {
		t.Helper()
		tx, err := types.SignNewTx(key, signer, &types.LegacyTx{Nonce: nonce, To: &to, Gas: 120_000, GasPrice: big.NewInt(1), Data: data})
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	approvalData := make([]byte, 68)
	copy(approvalData, approveSelector[:])
	copy(approvalData[16:36], flapPortal[:])
	approvalData[67] = 1
	approval := signed(0, token, approvalData)
	sale := signed(1, flapPortal, token[:])
	if flapPendingApproval(sale, []*types.Transaction{approval, sale}) != approval {
		t.Fatal("approval not selected from consecutive nonces")
	}

	run := func(sale, approval *types.Transaction) (*types.Receipt, error) {
		t.Helper()
		work := base.Copy()
		evm := vm.NewEVM(vm.BlockContext{
			CanTransfer: core.CanTransfer, Transfer: core.Transfer, BlockNumber: header.Number,
			GasLimit: header.GasLimit, Difficulty: header.Difficulty, BaseFee: big.NewInt(0),
		}, work, cfg, vm.Config{})
		msg, err := core.TransactionToMessage(sale, signer, header.BaseFee)
		if err != nil {
			t.Fatal(err)
		}
		return applyPendingTransactions(evm, work, header, signer, sale, msg, approval)
	}
	if _, err := run(sale, nil); !errors.Is(err, core.ErrNonceTooHigh) {
		t.Fatalf("sale alone must fail before execution: %v", err)
	}
	if receipt, err := run(signed(0, flapPortal, token[:]), nil); err != nil || receipt.Status != types.ReceiptStatusFailed {
		t.Fatalf("without the approval's state write the sale must revert: receipt=%v err=%v", receipt, err)
	}
	if receipt, err := run(sale, approval); err != nil || receipt == nil || receipt.Status != types.ReceiptStatusSuccessful ||
		receipt.TransactionIndex != 1 || !matchesFlapSale(receipt, token) {
		t.Fatalf("approval then sale did not produce the expected log: receipt=%v err=%v", receipt, err)
	}
	if receipt, err := run(signed(1, token, token[:]), approval); err != nil || receipt != nil {
		t.Fatalf("non-Portal action must stay outside the grouped stream: receipt=%v err=%v", receipt, err)
	}
}

func TestPendingFlapHistoricalApprovalSamples(t *testing.T) {
	path := os.Getenv("FLAP_APPROVAL_REPLAY_FIXTURE")
	if path == "" {
		t.Skip("requires the historical signed-transaction fixture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct {
		Block             int
		Token             common.Address
		ParentNonce       uint64
		SellHash          common.Hash
		SellRaw           hexutil.Bytes
		ApprovalHash      *common.Hash
		ApprovalRaw       *hexutil.Bytes
		SimulatedSellLogs []*types.Log
	}
	if err := json.Unmarshal(data, &samples); err != nil {
		t.Fatal(err)
	}
	if len(samples) != 12 {
		t.Fatalf("expected 12 samples, got %d", len(samples))
	}
	var paired int
	signer := types.LatestSignerForChainID(big.NewInt(56))
	for _, sample := range samples {
		t.Run(fmt.Sprint(sample.Block), func(t *testing.T) {
			var sell types.Transaction
			if err := sell.UnmarshalBinary(sample.SellRaw); err != nil || sell.Hash() != sample.SellHash {
				t.Fatalf("invalid signed sale: %v", err)
			}
			if !matchesFlapSale(&types.Receipt{Logs: sample.SimulatedSellLogs}, sample.Token) {
				t.Fatal("simulated sale does not match its Flap token")
			}
			if sample.ApprovalRaw == nil {
				if sell.Nonce() != sample.ParentNonce {
					t.Fatal("sale without an approval should have the parent nonce")
				}
				return
			}
			var approval types.Transaction
			if err := approval.UnmarshalBinary(*sample.ApprovalRaw); err != nil || sample.ApprovalHash == nil || approval.Hash() != *sample.ApprovalHash {
				t.Fatalf("invalid signed approval: %v", err)
			}
			seller, sellErr := types.Sender(signer, &sell)
			owner, approvalErr := types.Sender(signer, &approval)
			if sellErr != nil || approvalErr != nil || seller != owner || sell.Nonce() != sample.ParentNonce+1 {
				t.Fatalf("signed pair does not match sender and parent nonce: sell=%v approval=%v", sellErr, approvalErr)
			}
			if flapPendingApproval(&sell, []*types.Transaction{&approval, &sell}) != &approval {
				t.Fatal("prefix did not select the original approval")
			}
			paired++
		})
	}
	if paired != 9 {
		t.Fatalf("expected 9 original approval prefixes, got %d", paired)
	}
}
