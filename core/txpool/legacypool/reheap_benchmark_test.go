package legacypool

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// Fee/tip/type/nonce are real anonymized snapshot fields. Recipients, gas and
// payload layout are synthetic; this is not a replay of the live object graph.
func reheapRealTransactions(tb testing.TB, size int, seed int64) types.Transactions {
	tb.Helper()
	file, err := os.Open("testdata/reheap_pool_20261025.json.gz")
	if err != nil {
		tb.Fatal(err)
	}
	defer file.Close()
	stream, err := gzip.NewReader(file)
	if err != nil {
		tb.Fatal(err)
	}
	defer stream.Close()
	var rows []struct {
		Type  byte   `json:"type"`
		Nonce uint64 `json:"nonce"`
		Fee   string `json:"fee_cap_wei"`
		Tip   string `json:"tip_cap_wei"`
	}
	if err := json.NewDecoder(stream).Decode(&rows); err != nil {
		tb.Fatal(err)
	}
	if len(rows) != 39168 {
		tb.Fatalf("snapshot length %d", len(rows))
	}
	txs := make(types.Transactions, size)
	for i := range txs {
		r := rows[i%len(rows)]
		fee, ok := new(big.Int).SetString(r.Fee, 10)
		if !ok {
			tb.Fatal("invalid fee")
		}
		tip, ok := new(big.Int).SetString(r.Tip, 10)
		if !ok {
			tb.Fatal("invalid tip")
		}
		to := common.BigToAddress(big.NewInt(int64(i + 1)))
		switch r.Type {
		case types.LegacyTxType:
			txs[i] = types.NewTx(&types.LegacyTx{Nonce: r.Nonce, GasPrice: fee, Gas: 21000, To: &to})
		case types.AccessListTxType:
			txs[i] = types.NewTx(&types.AccessListTx{Nonce: r.Nonce, GasPrice: fee, Gas: 21000, To: &to})
		case types.DynamicFeeTxType:
			txs[i] = types.NewTx(&types.DynamicFeeTx{Nonce: r.Nonce, GasFeeCap: fee, GasTipCap: tip, Gas: 21000, To: &to})
		case types.SetCodeTxType:
			txs[i] = types.NewTx(&types.SetCodeTx{Nonce: r.Nonce, GasFeeCap: uint256.MustFromBig(fee), GasTipCap: uint256.MustFromBig(tip), Gas: 21000, To: to})
		default:
			tb.Fatalf("unexpected real type %d", r.Type)
		}
	}
	rand.New(rand.NewSource(seed)).Shuffle(len(txs), func(i, j int) { txs[i], txs[j] = txs[j], txs[i] })
	return txs
}

func reheapBenchmarkList(txs types.Transactions) *pricedList {
	all := newLookup()
	for _, tx := range txs {
		all.Add(tx)
	}
	list := newPricedList(all)
	list.SetBaseFee(new(big.Int))
	return list
}

func BenchmarkReheapRealPool(b *testing.B) {
	for _, size := range []int{39168, 80000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			p := reheapBenchmarkList(reheapRealTransactions(b, size, 25))
			b.ReportAllocs()
			for b.Loop() {
				p.Reheap()
			}
		})
	}
}
