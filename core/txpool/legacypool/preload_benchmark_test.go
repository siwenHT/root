package legacypool

import (
	"fmt"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// Immutable encoded accounts model the hot lower-level snapshot cache, not a
// replay of production state or physical disk IO. Every StateAt has empty objects.
type preloadReader struct {
	state.Reader
	accounts map[common.Hash][]byte
}

func (r *preloadReader) Account(addr common.Address) (*types.StateAccount, error) {
	if data := r.accounts[crypto.Keccak256Hash(addr[:])]; data != nil {
		return types.FullAccount(data)
	}
	return nil, nil
}

type preloadChain struct {
	BlockChain
	db state.Database
	reader state.Reader
}

func (c *preloadChain) StateAt(root common.Hash) (*state.StateDB, error) {
	return state.NewWithReader(root, c.db, c.reader)
}

// Implemented even in the baseline binary; the baseline doesn't consult it.
func (c *preloadChain) StateAtForPool(root common.Hash) (*state.StateDB, error) {
	return c.StateAt(root)
}

func preloadHead(old *types.Header, root common.Hash) *types.Header {
	return &types.Header{ParentHash: old.Hash(), Number: new(big.Int).Add(old.Number, common.Big1),
		Root: root, GasLimit: old.GasLimit, Time: old.Time + 1, Difficulty: new(big.Int), BaseFee: new(big.Int)}
}

func benchmarkPreloadPool(b *testing.B, accounts int) *LegacyPool {
	db := state.NewDatabaseForTesting()
	r := &preloadReader{accounts: make(map[common.Hash][]byte, accounts+832)}
	base, _ := state.New(types.EmptyRootHash, db)
	r.Reader = base.Reader()
	chain := &preloadChain{BlockChain: newTestBlockChain(params.TestChainConfig, 30000000, base, nil), db: db, reader: r}
	config := testTxPoolConfig
	config.GlobalSlots, config.GlobalQueue = 200000, 200000
	pool := New(config, chain)
	pool.reserver = newReserver()
	pool.currentHead.Store(chain.CurrentBlock())
	pool.currentState, _ = chain.StateAt(types.EmptyRootHash)
	pool.pendingNonces = newNoncer(pool.currentState)
	pool.gasTip.Store(uint256.NewInt(1))
	for i := 0; i < accounts+832; i++ {
		addr := common.BigToAddress(big.NewInt(int64(i+1)))
		r.accounts[crypto.Keccak256Hash(addr[:])] = types.SlimAccountRLP(types.StateAccount{
			Balance: uint256.NewInt(1000000000000000000), Root: types.EmptyRootHash, CodeHash: types.EmptyCodeHash.Bytes()})
		l := newList(i < accounts)
		count, nonce := 1, uint64(0)
		if i < 1170 { count = 2 } // 37,275 accounts / 38,445 pending txs.
		if i >= accounts { count, nonce = 2, 2 }
		if i >= accounts && i < accounts+309 { count = 3 }
		for j := 0; j < count; j++ {
			tx := types.NewTx(&types.LegacyTx{Nonce: nonce+uint64(j), To: &addr, Gas: 21000,
				GasPrice: big.NewInt(int64(i+1))})
			l.Add(tx, config.PriceBump)
			pool.all.Add(tx)
		}
		if i < accounts { pool.pending[addr] = l } else { pool.queue.queued[addr] = l }
	}
	pool.priced.Reheap()
	return pool
}

func BenchmarkResetPreload(b *testing.B) {
	for _, size := range []int{37275, 80000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			pool := benchmarkPreloadPool(b, size)
			var held, waited, resetNS, demoteNS int64
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
				old := pool.currentHead.Load()
				head := preloadHead(old, common.BigToHash(big.NewInt(int64(i+1))))
				done := make(chan struct{})
				b.StartTimer()
				pool.runReorg(done, &txpoolResetRequest{oldHead: old, newHead: head}, nil, nil)
				b.StopTimer()
				for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
					if e.Source == "runReorg" && e.Stage == "pool_lock_hold" { held += e.DurationUS*1000 }
					if e.Source == "runReorg" && e.Stage == "pool_lock_wait" { waited += e.DurationUS*1000 }
					if e.Source == "reset" && e.Stage == "pool_phase" { resetNS += e.DurationUS*1000 }
					if e.Source == "demote" && e.Stage == "pool_phase" { demoteNS += e.DurationUS*1000 }
				}
				if pool.currentHead.Load() != head || pool.all.Count() != size+1170+1973 { b.Fatal("reset changed fixture") }
				b.StartTimer()
			}
			b.StopTimer()
			b.ReportMetric(float64(held)/float64(b.N), "hold-ns/op")
			b.ReportMetric(float64(waited)/float64(b.N), "wait-ns/op")
			b.ReportMetric(float64(resetNS)/float64(b.N), "reset-ns/op")
			b.ReportMetric(float64(demoteNS)/float64(b.N), "demote-ns/op")
		})
	}
}
