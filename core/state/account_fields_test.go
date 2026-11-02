package state

import (
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

func TestBalanceAndNonceMatchesState(t *testing.T) {
	db := NewDatabaseForTesting()
	original, _ := New(types.EmptyRootHash, db)
	addr := common.HexToAddress("0x1234")
	original.SetBalance(addr, uint256.NewInt(12345), tracing.BalanceChangeUnspecified)
	original.SetNonce(addr, 7, tracing.NonceChangeUnspecified)
	root, err := original.Commit(0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(root, db)
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		balance, nonce := s.GetBalanceAndNonce(addr)
		if balance.Cmp(s.GetBalance(addr)) != 0 || nonce != s.GetNonce(addr) {
			t.Fatal("account fields differ")
		}
	}
	balance, nonce := s.GetBalanceAndNonce(addr)
	if balance.Uint64() != 12345 || nonce != 7 || len(s.stateObjects) != 0 {
		t.Fatal("cold read populated state objects or lost fields")
	}
	check()
	snap := s.Snapshot()
	s.SetBalance(addr, uint256.NewInt(456), tracing.BalanceChangeUnspecified)
	s.SetNonce(addr, 8, tracing.NonceChangeUnspecified)
	check()
	s.RevertToSnapshot(snap)
	check()
	s.SelfDestruct(addr)
	check()
	s.Finalise(true)
	check()
	balance, nonce = s.GetBalanceAndNonce(common.HexToAddress("0x9999"))
	if !balance.IsZero() || nonce != 0 || s.Error() != nil {
		t.Fatal("absent account read failed")
	}
	fresh, _ := New(root, db)
	balance, nonce = fresh.GetBalanceAndNonce(addr)
	if balance.Uint64() != 12345 || nonce != 7 {
		t.Fatal("read leaked mutations across state views")
	}
}

func BenchmarkAccountFields(b *testing.B) {
	for _, size := range []int{38_874, 80_000} {
		db := NewDatabaseForTesting()
		original, _ := New(types.EmptyRootHash, db)
		addresses := make([]common.Address, size)
		for i := range addresses {
			binary.BigEndian.PutUint64(addresses[i][12:], uint64(i+1))
			original.SetBalance(addresses[i], uint256.NewInt(1000000), tracing.BalanceChangeUnspecified)
			original.SetNonce(addresses[i], 1, tracing.NonceChangeUnspecified)
		}
		root, err := original.Commit(0, false, false)
		if err != nil {
			b.Fatal(err)
		}
		for _, combined := range []bool{false, true} {
			b.Run(fmt.Sprintf("accounts%d/combined%t", size, combined), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					s, err := New(root, db)
					if err != nil {
						b.Fatal(err)
					}
					for _, addr := range addresses {
						if combined {
							s.GetBalanceAndNonce(addr)
						} else {
							s.GetNonce(addr)
							s.GetBalance(addr)
						}
					}
					if s.Error() != nil {
						b.Fatal(s.Error())
					}
				}
			})
		}
	}
}

type accountFieldsErrorReader struct {
	Reader
	err error
}

func (r accountFieldsErrorReader) Account(common.Address) (*types.StateAccount, error) {
	return nil, r.err
}
func TestBalanceAndNoncePreservesReadError(t *testing.T) {
	s, _ := New(types.EmptyRootHash, NewDatabaseForTesting())
	want := errors.New("account read failed")
	s.reader = accountFieldsErrorReader{Reader: s.Reader(), err: want}
	balance, nonce := s.GetBalanceAndNonce(common.HexToAddress("0x1234"))
	if !balance.IsZero() || nonce != 0 || !errors.Is(s.Error(), want) {
		t.Fatal("account read error was lost")
	}
}
