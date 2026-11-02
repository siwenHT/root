# Account field reads and below-limit pending truncation, 2026-11-02

The node changes 30 runtime lines in two files. Demotion now reads nonce and
balance together without constructing full EVM state objects for cold accounts.
Cached in-memory mutations/deletions take precedence; absent accounts and reader
errors retain the existing zero/error behavior. The read is under the existing
pool lock at the existing root. No preparation thread, cross-root cache or new
lock is introduced. Callers must not modify the returned balance.

`truncatePending` returns early only when the entire lookup count (pending plus
queued) is at or below GlobalSlots. Otherwise the original counting, fairness
and eviction algorithm runs. This uses an existing upper bound and adds no
transaction counter to maintain. Production configuration observed before this
release: GlobalSlots=150000; pending approximately 70000 and queued approximately
2100. Configuration is unchanged by this change.

Validation: complete core/state, core/txpool/legacypool, core/txpool, core/types,
eth, internal/ethapi and rpc packages under Linux Go 1.25 CGO; focused race tests.
The new tests cover cold reads without state objects, cached mutations, snapshot
revert, destruction, missing accounts, root isolation, reader errors and actual
pending/queued promotion/truncation at below/equal/over-limit counts.

The complete Reset benchmark reuses the immutable encoded-account fixture from
commit 21b6340, calling the actual Reset scheduler and waiting for completion.
Three series of five iterations per version gave median wall times:
37275 pending accounts: 256.09 -> 172.40ms; 80000: 586.31 -> 414.84ms.
Allocation bytes per reset fell approximately 60% and 62%. This fixture models a
hot encoded-account reader, not physical disk IO. A separate real-trie benchmark
at 38874/80000 accounts gave 243.48 -> 177.17ms and 610.07 -> 392.93ms.
Below-limit truncation at 38874/80000 accounts fell from 1.95/7.48ms to about
20ns with no allocation. Benchmarks are before-then-after, not randomized trials.

The historical state-preload implementation is not restored. Its CPU budget
failure does not apply as evidence for this distinct implementation. Production
latency is measured separately with the existing 300s collector and same hash
sampling/subscription correlation; results are recorded in the engine repository
protocol/v5/pending-state-truncate-20261102/README.md after release.

Design references inspected before implementation: the existing LGPL state
reader and cached-object behavior in this repository, upstream
[StateDB](https://github.com/ethereum/go-ethereum/blob/master/core/state/statedb.go)
and [legacy pool](https://github.com/ethereum/go-ethereum/blob/master/core/txpool/legacypool/legacypool.go).
No upstream code is copied. The reset fixture is reused from this repository.
