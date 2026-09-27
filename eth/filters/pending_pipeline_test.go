package filters

import (
	"context"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

type pendingPipelineBackend struct {
	*testBackend
	simulate func(context.Context, *types.Transaction) (*types.Receipt, error)
}

func (b *pendingPipelineBackend) CurrentHeader() *types.Header {
	return &types.Header{Number: big.NewInt(1)}
}

func (b *pendingPipelineBackend) SimulateTransaction(ctx context.Context, tx *types.Transaction) (*types.Receipt, error) {
	return b.simulate(ctx, tx)
}

func pendingPipelineClient(t *testing.T, simulate func(context.Context, *types.Transaction) (*types.Receipt, error)) (*pendingPipelineBackend, <-chan common.Hash, *rpc.ClientSubscription) {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	t.Cleanup(func() { db.Close() })
	b := &pendingPipelineBackend{testBackend: &testBackend{db: db}, simulate: simulate}
	api := NewFilterAPI(NewFilterSystem(b, Config{}), false)
	t.Cleanup(api.events.txsSub.Unsubscribe)
	server := rpc.NewServer()
	if err := server.RegisterName("eth", api); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	client := rpc.DialInProc(server)
	t.Cleanup(client.Close)
	notifications := make(chan common.Hash, 128)
	sub, err := client.EthSubscribe(context.Background(), notifications, "newPendingTransactions", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Unsubscribe)
	return b, notifications, sub
}

func pendingPipelineTx(n uint64) *types.Transaction {
	return types.NewTx(&types.LegacyTx{Nonce: n, Gas: 21000, GasPrice: big.NewInt(1)})
}

func TestPendingPipelineCrossBatchAndDedup(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var slowCalls atomic.Int32
	b, notifications, _ := pendingPipelineClient(t, func(ctx context.Context, tx *types.Transaction) (*types.Receipt, error) {
		if tx.Nonce() == 1 {
			slowCalls.Add(1)
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &types.Receipt{Status: types.ReceiptStatusSuccessful}, nil
	})
	slow, fast := pendingPipelineTx(1), pendingPipelineTx(2)
	b.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{slow}})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first simulation did not start")
	}
	b.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{slow, fast, fast}})
	select {
	case hash := <-notifications:
		if hash != fast.Hash() {
			t.Fatalf("want fast transaction before slow completion, got %s", hash)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("next batch blocked behind unfinished previous batch")
	}
	close(release)
	select {
	case hash := <-notifications:
		if hash != slow.Hash() {
			t.Fatalf("want slow transaction once, got %s", hash)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow transaction not delivered after release")
	}
	if slowCalls.Load() != 1 {
		t.Fatalf("duplicate in-flight simulation: %d", slowCalls.Load())
	}
	last := pendingPipelineTx(3)
	b.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{slow, fast, last}})
	select {
	case hash := <-notifications:
		if hash != last.Hash() {
			t.Fatalf("successful transaction was delivered twice: %s", hash)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("final batch not delivered")
	}
}

func TestPendingPipelineBoundedAndUnsubscribe(t *testing.T) {
	started := make(chan struct{}, 64)
	canceled := make(chan struct{}, 64)
	b, _, sub := pendingPipelineClient(t, func(ctx context.Context, tx *types.Transaction) (*types.Receipt, error) {
		started <- struct{}{}
		<-ctx.Done()
		canceled <- struct{}{}
		return nil, ctx.Err()
	})
	var batch []*types.Transaction
	for n := uint64(0); n < 64; n++ {
		batch = append(batch, pendingPipelineTx(n))
	}
	b.txFeed.Send(core.NewTxsEvent{Txs: batch})
	for n := 0; n < 24; n++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d simulations started", n)
		}
	}
	select {
	case <-started:
		t.Fatal("more than 24 concurrent simulations")
	case <-time.After(50 * time.Millisecond):
	}
	sub.Unsubscribe()
	for n := 0; n < 24; n++ {
		select {
		case <-canceled:
		case <-time.After(3 * time.Second):
			t.Fatal("unsubscribe did not cancel in-flight simulations")
		}
	}
}
