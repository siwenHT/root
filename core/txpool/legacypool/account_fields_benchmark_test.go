package legacypool

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// Fixture reused from the 2026-10-25 reset benchmark. Only the actual scheduler
// and reset completion are measured; no preload implementation is restored.
type fieldsReader struct {
	state.Reader
	accounts map[common.Hash][]byte
}

func (r *fieldsReader) Account(addr common.Address) (*types.StateAccount, error) {
	if data := r.accounts[crypto.Keccak256Hash(addr[:])]; data != nil {
		return types.FullAccount(data)
	}
	return nil, nil
}

type fieldsChain struct {
	BlockChain
	db     state.Database
	reader state.Reader
	trie   bool
}

func (c *fieldsChain) StateAt(header *types.Header) (*state.StateDB, error) {
	if c.trie {
		return state.New(header.Root, c.db)
	}
	return state.NewWithReader(header.Root, c.db, c.reader)
}

func fieldsHead(old *types.Header, root common.Hash) *types.Header {
	return &types.Header{ParentHash: old.Hash(), Number: new(big.Int).Add(old.Number, common.Big1),
		Root: root, GasLimit: old.GasLimit, Time: old.Time + 1, Difficulty: new(big.Int), BaseFee: new(big.Int)}
}

func benchmarkFieldsPool(b *testing.B, accounts int) *LegacyPool {
	db := state.NewDatabaseForTesting()
	r := &fieldsReader{accounts: make(map[common.Hash][]byte, accounts+832)}
	base, _ := state.New(types.EmptyRootHash, db)
	r.Reader = base.Reader()
	chain := &fieldsChain{BlockChain: newTestBlockChain(params.TestChainConfig, 30000000, base, nil), db: db, reader: r}
	config := testTxPoolConfig
	config.GlobalSlots, config.GlobalQueue = 200000, 200000
	pool := New(config, chain)
	pool.reserver = newReserver()
	pool.currentHead.Store(chain.CurrentBlock())
	pool.currentState, _ = chain.StateAt(&types.Header{Root: types.EmptyRootHash})
	pool.pendingNonces = newNoncer(pool.currentState)
	pool.gasTip.Store(uint256.NewInt(1))
	for i := 0; i < accounts+832; i++ {
		addr := common.BigToAddress(big.NewInt(int64(i + 1)))
		r.accounts[crypto.Keccak256Hash(addr[:])] = types.SlimAccountRLP(types.StateAccount{
			Balance: uint256.NewInt(1000000000000000000), Root: types.EmptyRootHash, CodeHash: types.EmptyCodeHash.Bytes()})
		l := newList(i < accounts)
		count, nonce := 1, uint64(0)
		if i < 1170 {
			count = 2
		} // 37,275 accounts / 38,445 pending txs.
		if i >= accounts {
			count, nonce = 2, 2
		}
		if i >= accounts && i < accounts+309 {
			count = 3
		}
		for j := 0; j < count; j++ {
			tx := types.NewTx(&types.LegacyTx{Nonce: nonce + uint64(j), To: &addr, Gas: 21000,
				GasPrice: big.NewInt(int64(i + 1))})
			l.Add(tx, config.PriceBump)
			pool.all.Add(tx)
		}
		if i < accounts {
			pool.pending[addr] = l
		} else {
			pool.queue.queued[addr] = l
		}
	}
	pool.priced.Reheap()
	return pool
}

func BenchmarkAccountFieldsReset(b *testing.B) {
	for _, size := range []int{37_275, 80_000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			pool := benchmarkFieldsPool(b, size)
			pool.wg.Add(1)
			go pool.scheduleReorgLoop()
			defer func() { close(pool.reorgShutdownCh); pool.wg.Wait() }()
			b.ReportAllocs()
			for b.Loop() {
				old := pool.currentHead.Load()
				pool.Reset(old, fieldsHead(old, types.EmptyRootHash))
			}
		})
	}
}
