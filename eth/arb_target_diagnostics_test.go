package eth

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/arb"
)

func TestBoundedRevertDataPreservesLengthAndCapsPayload(t *testing.T) {
	full := bytes.Repeat([]byte{0xab}, maxDiagnosticRevertData+17)
	got, n := boundedRevertData(&core.ExecutionResult{Err: vm.ErrExecutionReverted, ReturnData: full})
	if n != len(full) {
		t.Fatalf("revert length=%d want %d", n, len(full))
	}
	if len(got) != maxDiagnosticRevertData {
		t.Fatalf("bounded payload length=%d want %d", len(got), maxDiagnosticRevertData)
	}
	if !bytes.Equal(got, full[:maxDiagnosticRevertData]) {
		t.Fatal("bounded payload was not a prefix of the EVM return data")
	}
	if got, n := boundedRevertData(&core.ExecutionResult{Err: vm.ErrOutOfGas, ReturnData: full}); got != nil || n != 0 {
		t.Fatalf("out-of-gas must not be reported as revert data: len=%d n=%d", len(got), n)
	}
}

func TestRunCandidateCapturesTopLevelRevertData(t *testing.T) {
	x, _, bc := prefixFixture(t)
	defer bc.Stop()

	// REVERT with a 32-byte payload ending in 0xdeadbeef.
	contract := common.BytesToAddress([]byte("revert-contract"))
	x.base.CreateAccount(contract)
	x.base.SetCode(contract, []byte{0x63, 0xde, 0xad, 0xbe, 0xef, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xfd}, tracing.CodeChangeUnspecified)

	ours := candidateEnvForKey1(0, contract, big.NewInt(0), x.baseFee())
	ours.GasLimit = 100_000
	res, err := x.RunCandidate(nil, ours, PurposeDiagnostic, newReadBudget(1_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !res.PrefixCompleted || res.Candidate == nil {
		t.Fatalf("direct candidate should run: prefix=%v candidate=%v", res.PrefixCompleted, res.Candidate != nil)
	}
	out := res.Candidate
	if out.Class.Status != arb.StatusReverted {
		t.Fatalf("candidate status=%s want reverted", out.Class.Status)
	}
	if out.RevertDataLen != 32 || len(out.RevertData) != 32 {
		t.Fatalf("revert data length=(%d,%d) want (32,32)", out.RevertDataLen, len(out.RevertData))
	}
	if !bytes.Equal(out.RevertData[28:], []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Fatalf("unexpected revert payload suffix %x", out.RevertData[28:])
	}
}
