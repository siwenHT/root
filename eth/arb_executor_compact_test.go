package eth

import (
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestExecutorCompactReturnLedgerWithoutLogs(t *testing.T) {
	executor := common.HexToAddress("0x1234")
	mt := measureTarget{measure: true, executor: executor, baseToken: directWBNB}
	data := make([]byte, compactHeaderSize)
	copy(data, executeCompactSelector)
	binary.BigEndian.PutUint32(data[4:8], 1_500_000)
	binary.BigEndian.PutUint16(data[8:10], 9300)
	if err := validateExecutorEnvelope(&executor, data, 1_500_000, mt); err != nil {
		t.Fatal(err)
	}
	returned := make([]byte, 96)
	big.NewInt(100).FillBytes(returned[:32])
	big.NewInt(1100).FillBytes(returned[32:64])
	big.NewInt(200).FillBytes(returned[64:])
	receipt := &types.Receipt{Status: types.ReceiptStatusSuccessful, GasUsed: 162421}
	ledger, err := executorLedger(receipt, data, returned, mt)
	if err != nil {
		t.Fatal(err)
	}
	if ledger["measurement"] != "executor_return" || ledger["executor_revision"] != executorRevisionCompact ||
		ledger["retained_t0"] != "100" || ledger["builder_payment"] != "900" ||
		ledger["executor_gas_used"] != "162421" || ledger["owner_sweep"] != "0" {
		t.Fatalf("unexpected compact ledger: %v", ledger)
	}
	if revision, ok := executorRevisionForData(data); !ok || revision != executorRevisionCompact {
		t.Fatal("compact revision missing")
	}
	if err := validateExecutorEnvelope(&executor, data, 1_500_001, mt); err == nil {
		t.Fatal("gas mismatch accepted")
	}
	if _, err := executorLedger(receipt, data, returned[:95], mt); err == nil {
		t.Fatal("truncated return accepted")
	}
	big.NewInt(99).FillBytes(returned[64:])
	if _, err := executorLedger(receipt, data, returned, mt); err == nil {
		t.Fatal("inventory loss accepted")
	}
	big.NewInt(101).FillBytes(returned[64:])
	if _, err := executorLedger(receipt, data, returned, mt); err == nil {
		t.Fatal("builder share violation accepted")
	}
	receipt.Status = types.ReceiptStatusFailed
	big.NewInt(200).FillBytes(returned[64:])
	if _, err := executorLedger(receipt, data, returned, mt); err == nil {
		t.Fatal("reverted return accepted")
	}
}
