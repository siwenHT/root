package legacypool

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/pendingtrace"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
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
	db     state.Database
	reader state.Reader
	trie   bool
}

func (c *preloadChain) StateAt(root common.Hash) (*state.StateDB, error) {
	if c.trie {
		return state.New(root, c.db)
	}
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

// Linux process ticks include concurrent GC CPU. The runner records CLK_TCK;
// quantization is 10ms on the measurement host, so use multiple paired rounds.
func preloadCPUTicks() uint64 {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	text := string(data)
	fields := strings.Fields(text[strings.LastIndex(text, ")")+1:])
	u, _ := strconv.ParseUint(fields[11], 10, 64)
	s, _ := strconv.ParseUint(fields[12], 10, 64)
	return u + s
}

func BenchmarkResetPreload(b *testing.B)        { benchmarkResetPreload(b, false, false) }
func BenchmarkResetPreloadSampled(b *testing.B) { benchmarkResetPreload(b, true, false) }
func BenchmarkResetPreloadTrie(b *testing.B)    { benchmarkResetPreload(b, false, true) }

func benchmarkResetPreload(b *testing.B, sampled, trie bool) {
	for _, size := range []int{37275, 80000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			pool := benchmarkPreloadPool(b, size)
			if trie {
				chain := pool.chain.(*preloadChain)
				ledger, err := state.New(types.EmptyRootHash, chain.db)
				if err != nil {
					b.Fatal(err)
				}
				for addr := range pool.pending {
					ledger.AddBalance(addr, uint256.NewInt(1000000000000000000), tracing.BalanceChangeUnspecified)
				}
				for addr := range pool.queue.queued {
					ledger.AddBalance(addr, uint256.NewInt(1000000000000000000), tracing.BalanceChangeUnspecified)
				}
				root, err := ledger.Commit(0, true, false)
				if err != nil {
					b.Fatal(err)
				}
				if err := chain.db.TrieDB().Commit(root, false); err != nil {
					b.Fatal(err)
				}
				chain.trie = true
				head := types.CopyHeader(pool.currentHead.Load())
				head.Root = root
				pool.currentHead.Store(head)
				pool.currentState, err = chain.StateAt(root)
				if err != nil {
					b.Fatal(err)
				}
				pool.pendingNonces = newNoncer(pool.currentState)
			}
			// Include the actual scheduler handoff, queue wait and Reset's done
			// channel in caller-visible head-reset latency, not just runReorg.
			pool.wg.Add(1)
			go pool.scheduleReorgLoop()
			defer pool.Close()
			var held, waited, resetNS, demoteNS int64
			var cpuTicks, pause uint64
			var gc uint32
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if sampled {
					pool.reheapSequence = 31
				}
				before := pendingtrace.Snapshot(math.MaxUint64, 1).Latest
				old := pool.currentHead.Load()
				head := preloadHead(old, common.BigToHash(big.NewInt(int64(i+1))))
				if trie {
					head.Root = old.Root
				}
				var memBefore, memAfter runtime.MemStats
				runtime.ReadMemStats(&memBefore)
				cpuBefore := preloadCPUTicks()
				b.StartTimer()
				begin := time.Now()
				pool.Reset(old, head)
				total := time.Since(begin)
				b.StopTimer()
				cpuTicks += preloadCPUTicks() - cpuBefore
				runtime.ReadMemStats(&memAfter)
				gc += memAfter.NumGC - memBefore.NumGC
				pause += memAfter.PauseTotalNs - memBefore.PauseTotalNs
				var roundHold int64
				for _, e := range pendingtrace.Snapshot(before, pendingtrace.MaxPage).Events {
					if e.Scope == "preload" {
						data, _ := json.Marshal(e)
						b.Logf("PRELOAD_SAMPLE %s", data)
					}
					if e.Source == "runReorg" && e.Stage == "pool_lock_hold" {
						held += e.DurationUS * 1000
						roundHold = e.DurationUS * 1000
					}
					if e.Source == "runReorg" && e.Stage == "pool_lock_wait" {
						waited += e.DurationUS * 1000
					}
					if e.Source == "reset" && e.Stage == "pool_phase" {
						resetNS += e.DurationUS * 1000
					}
					if e.Source == "demote" && e.Stage == "pool_phase" {
						demoteNS += e.DurationUS * 1000
					}
				}
				b.Logf("RESET_SAMPLE size=%d total_ns=%d hold_ns=%d", size, total.Nanoseconds(), roundHold)
				if pool.currentHead.Load() != head || pool.all.Count() != size+1170+1973 {
					b.Fatal("reset changed fixture")
				}
				b.StartTimer()
			}
			b.StopTimer()
			b.ReportMetric(float64(held)/float64(b.N), "hold-ns/op")
			b.ReportMetric(float64(waited)/float64(b.N), "wait-ns/op")
			b.ReportMetric(float64(resetNS)/float64(b.N), "reset-ns/op")
			b.ReportMetric(float64(demoteNS)/float64(b.N), "demote-ns/op")
			b.ReportMetric(float64(cpuTicks)*1e7/float64(b.N), "cpu-ns/op")
			b.ReportMetric(float64(gc)/float64(b.N), "gc/op")
			b.ReportMetric(float64(pause)/float64(b.N), "gc-pause-ns/op")
		})
	}
}
