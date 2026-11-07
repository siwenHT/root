package eth

import (
	"bytes"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

var (
	flapPortal      = common.HexToAddress("0xe2ce6ab80874fa9fa2aae65d277dd6b8e65c9de0")
	flapPortalSell  = common.HexToHash("0x03a4693e592f5e75dc7c136acb39b146d2b4966c0e509c34f362dee02b3b861a")
	approveSelector = [4]byte{0x09, 0x5e, 0xa7, 0xb3}
)

// The pool entries are already grouped by the recovered sender of the sale.
func flapPendingApproval(sale *types.Transaction, pending []*types.Transaction) *types.Transaction {
	for _, tx := range pending {
		if tx.Nonce() != sale.Nonce()-1 || tx.To() == nil {
			continue
		}
		token := *tx.To()
		if token[18] != token[19] || (token[19] != 0x77 && token[19] != 0x88) {
			return nil
		}
		data := tx.Data()
		if len(data) == 68 && bytes.Equal(data[:4], approveSelector[:]) &&
			common.BytesToAddress(data[4:36]) != (common.Address{}) &&
			common.BytesToHash(data[36:68]) != (common.Hash{}) &&
			bytes.Contains(sale.Data(), token[:]) {
			return tx
		}
		return nil
	}
	return nil
}

func matchesFlapSale(receipt *types.Receipt, token common.Address) bool {
	for _, event := range receipt.Logs {
		if event.Address == flapPortal && len(event.Topics) != 0 && event.Topics[0] == flapPortalSell &&
			len(event.Data) >= 7*32 && common.BytesToAddress(event.Data[32:64]) == token {
			return true
		}
	}
	return false
}

func applyPendingTransactions(evm *vm.EVM, state *state.StateDB, header *types.Header, signer types.Signer, sale *types.Transaction, saleMsg *core.Message, approval *types.Transaction) (*types.Receipt, error) {
	gasPool := new(core.GasPool).AddGas(header.GasLimit)
	var usedGas uint64
	apply := func(tx *types.Transaction, msg *core.Message, index int) (*types.Receipt, error) {
		evm.SetTxContext(core.NewEVMTxContext(msg))
		state.SetTxContext(tx.Hash(), index)
		return core.ApplyTransactionWithEVM(msg, gasPool, state, header.Number, common.Hash{}, header.Time, tx, &usedGas, evm)
	}
	saleIndex := 0
	if approval != nil {
		msg, err := core.TransactionToMessage(approval, signer, header.BaseFee)
		if err != nil {
			return nil, err
		}
		receipt, err := apply(approval, msg, 0)
		if err != nil || receipt.Status != types.ReceiptStatusSuccessful {
			return nil, err
		}
		saleIndex = 1
	}
	receipt, err := apply(sale, saleMsg, saleIndex)
	if err == nil && approval != nil && !matchesFlapSale(receipt, *approval.To()) {
		return nil, nil
	}
	return receipt, err
}
