package filters

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

type flapApprovalStreamBackend struct {
	*pendingPipelineBackend
	approval *types.Transaction
}

func (b *flapApprovalStreamBackend) CurrentHeader() *types.Header {
	return &types.Header{Number: big.NewInt(1), BaseFee: big.NewInt(0), GasLimit: 10_000_000}
}

func (b *flapApprovalStreamBackend) SimulateFlapPendingTransaction(_ context.Context, _ *types.Transaction) (*types.Receipt, *types.Transaction, error) {
	approval := b.approval
	b.approval = nil
	return &types.Receipt{Status: types.ReceiptStatusSuccessful}, approval, nil
}

func TestPendingFlapApprovalNotification(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	t.Cleanup(func() { db.Close() })
	token := common.HexToAddress("0x7777")
	approval := types.NewTx(&types.LegacyTx{Nonce: 1, To: &token, Gas: 21_000, GasPrice: big.NewInt(1)})
	sale := pendingPipelineTx(2)
	backend := &flapApprovalStreamBackend{
		pendingPipelineBackend: &pendingPipelineBackend{testBackend: &testBackend{db: db}},
		approval:               approval,
	}
	api := NewFilterAPI(NewFilterSystem(backend, Config{}), false)
	t.Cleanup(api.events.txsSub.Unsubscribe)
	server := rpc.NewServer()
	if err := server.RegisterName("eth", api); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	client := rpc.DialInProc(server)
	t.Cleanup(client.Close)
	notifications := make(chan json.RawMessage, 1)
	sub, err := client.EthSubscribe(context.Background(), notifications, "newPendingTransactionsWithFlapApprovals")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Unsubscribe)

	for _, paired := range []bool{true, false} {
		backend.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{sale}})
		select {
		case notification := <-notifications:
			var got flapPendingTransactionRPC
			if err := json.Unmarshal(notification, &got); err != nil {
				t.Fatal(err)
			}
			saleRaw, _ := sale.MarshalBinary()
			if got.RPCTransaction == nil || got.RPCTransaction.Hash != sale.Hash() || !bytes.Equal(got.RPCTransaction.Raw, saleRaw) {
				t.Fatal("notification must retain the original sale")
			}
			if paired {
				approvalRaw, _ := approval.MarshalBinary()
				if got.Approval.Hash != approval.Hash() || got.Approval.Token != token || !bytes.Equal(got.Approval.Raw, approvalRaw) {
					t.Fatal("notification must retain the original approval")
				}
			} else if bytes.Contains(notification, []byte("flapApproval")) {
				t.Fatal("the standalone sale must not carry the mined approval")
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("missing notification with paired=%t", paired)
		}
	}
}
