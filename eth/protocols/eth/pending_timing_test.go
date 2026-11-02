package eth

import (
	"bytes"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestPendingTimingReceiveMetadataDoesNotChangeRLP(t *testing.T) {
	txs := []*types.Transaction{types.NewTx(&types.LegacyTx{Nonce: 7})}
	list, err := rlp.EncodeToRawList(txs)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	broadcast := &TransactionsPacket{RawList: list, receivedAt: at}
	got, err := rlp.EncodeToBytes(broadcast)
	if err != nil || !bytes.Equal(got, list.Bytes()) || broadcast.ReceivedAt() != at {
		t.Fatalf("broadcast wire encoding changed: %x, %v", got, err)
	}
	reply := &PooledTransactionsPacket{RequestId: 99, List: list, receivedAt: at}
	got, err = rlp.EncodeToBytes(reply)
	want, _ := rlp.EncodeToBytes(&PooledTransactionsPacket{RequestId: 99, List: list})
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("reply wire encoding changed: %x, %v", got, err)
	}
	var decoded PooledTransactionsPacket
	if err := rlp.DecodeBytes(got, &decoded); err != nil || decoded.RequestId != 99 || !decoded.ReceivedAt().IsZero() {
		t.Fatalf("local timestamp leaked into received packet: %+v, %v", decoded, err)
	}
}
