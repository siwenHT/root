package eth

import (
	"bytes"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/pendingtrace"
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
	packet := pendingtrace.NewPacket(pendingtrace.PacketMeta{Connection: 1, Number: 1})
	broadcast := &TransactionsPacket{RawList: list, receivedAt: at, trace: packet}
	got, err := rlp.EncodeToBytes(broadcast)
	if err != nil || !bytes.Equal(got, list.Bytes()) || broadcast.ReceivedAt() != at {
		t.Fatalf("broadcast wire encoding changed: %x, %v", got, err)
	}
	reply := &PooledTransactionsPacket{RequestId: 99, List: list, receivedAt: at, trace: packet}
	got, err = rlp.EncodeToBytes(reply)
	want, _ := rlp.EncodeToBytes(&PooledTransactionsPacket{RequestId: 99, List: list})
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("reply wire encoding changed: %x, %v", got, err)
	}
	var decoded PooledTransactionsPacket
	if err := rlp.DecodeBytes(got, &decoded); err != nil || decoded.RequestId != 99 || !decoded.ReceivedAt().IsZero() || decoded.PendingPacket() != nil {
		t.Fatalf("local timestamp leaked into received packet: %+v, %v", decoded, err)
	}
	ann := &NewPooledTransactionHashesPacket{trace: packet}
	got, err = rlp.EncodeToBytes(ann)
	want, _ = rlp.EncodeToBytes(&NewPooledTransactionHashesPacket{})
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("hash announcement metadata changed wire encoding")
	}
}
