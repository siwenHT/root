package legacypool

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func BenchmarkReheapDiagnostics(b *testing.B) {
	for _, size := range []int{39168, 80000} {
		for _, mode := range []string{"sampled", "one_in_32"} {
			b.Run(fmt.Sprintf("%s/%d", mode, size), func(b *testing.B) {
				txs := reheapRealTransactions(b, size, 25)
				pool := &LegacyPool{priced: reheapBenchmarkList(txs), queue: newQueue(DefaultConfig, nil), pending: make(map[common.Address]*list)}
				pool.traceID.Store(1)
				// Match the observed queued-account scan size; transaction objects
				// remain shared with the benchmark heap and are not timed setup.
				for i := 0; i < 473; i++ {
					q := newList(false)
					q.Add(txs[i], 10)
					pool.queue.queued[common.BigToAddress(big.NewInt(int64(i+1)))] = q
				}
				head := &types.Header{Number: big.NewInt(100), BaseFee: new(big.Int)}
				lock := poolLockTiming{pool: pool, id: 1}
				rebuildsPerOp := 1
				if mode == "one_in_32" {
					rebuildsPerOp = 32
				}
				var samples, comparisons uint64
				b.ReportAllocs()
				for b.Loop() {
					// A whole cycle guarantees exactly 1/32 sampling even for
					// short runs; normalize time/allocations by rebuilds/op.
					for i := 0; i < rebuildsPerOp; i++ {
						if mode == "sampled" {
							pool.reheapSequence = 31
						}
						d := pool.sampleReheap(head)
						pool.priced.reheap(d)
						d.publish(lock)
						if d != nil {
							samples++
							for _, stage := range d.stats.Stages {
								comparisons += stage.Comparisons
							}
						}
					}
				}
				b.ReportMetric(float64(rebuildsPerOp), "rebuilds/op")
				b.ReportMetric(float64(samples)/float64(b.N*rebuildsPerOp), "samples/rebuild")
				b.ReportMetric(float64(comparisons)/float64(samples), "comparisons/sample")
			})
		}
	}
}
