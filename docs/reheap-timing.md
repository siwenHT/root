# Sampled price-heap rebuild diagnostics

2026-10-28: this experimental instrumentation has been removed from main at the user's request. Historical measurements below remain dated records. See [rollback](experimental-rollback-20261028.md).

The locally validated StateDB preparation change and its release status are
documented in [txpool state preload](txpool-state-preload.md).

These diagnostics add no configuration or RPC endpoint and do not change heap
policy, comparison results, the 4:1 heap ratio or rebuild frequency.

Only head-reset rebuilds are eligible. Under the existing pool lock a per-pool
counter advances for each actual rebuild, including an empty pool. Rounds 32,
64, ... collect a complete sample. Promotion-only calls and missing new heads
do not advance it. The requested new head is recorded; join `state_open` by
parent operation to identify a failed state reset rather than assuming it worked.
Stale-triggered `Reheap` calls outside head reset retain the normal path and are
outside this denominator. Sampling is systematic rather than random; a short
capture may miss a transient round.

Normal `priceHeap.Less/cmp` and `Transaction.EffectiveGasTipCmp` are unchanged.
Sampled rounds use a heap wrapper and `EffectiveGasTipCmpWithPath`, which mirrors
the comparator and returns the branch actually taken. Tests compare results,
branch classification and exact comparison counts. There is no global atomic,
clock read or log write per comparison. Sampled rounds still incur observer cost;
their wall durations must not be treated as uninstrumented measurements.

## Wire format and correlation

`debug_pendingTiming` keeps page schema 2 (additive fields). An event with
`scope=reheap`, `stage=priced_reheap`, `source=head_reset` carries `reheap.version=1`.
Join `boot_id + pool + parent_operation` to the enclosing `runReorg` lock/phase
operation. `operation` identifies this rebuild, `parent` is the requested head
hash, and `reheap.head` its number. IDs are local to one boot.

The nested payload includes:

- `rebuild`: per-pool head-rebuild denominator; `sample_every=32`.
- Input transaction count, derived pending count, queued count, account counts,
  type histogram, stale count before rebuilding, and before/after heap sizes.
- `mutex_wait_us`, `lookup_us`, `total_us`, and three `stages` in fixed order:
  urgent init, urgent pops/split, floating init. `total_us` excludes mutex wait;
  the event span includes it. These are wall durations, not CPU time.
- Per stage: comparisons, actual nil/zero-fast/zero-guard-fallback/nonzero
  branches, equal-tip/fee/tip-cap fallthroughs and nonce comparisons/full ties.
- Ordered comparison-weighted `type_pairs[6][6]`. Buckets 0..4 are transaction
  type IDs; bucket 5 is other. `type_counts[6]` uses the same mapping.

For each stage, comparisons equal the sum of branch counts and the type-pair
matrix. The nil-base-fee count means the heap skipped effective-tip comparison
and compared fee caps directly; it does not mean `EffectiveGasTipCmp(nil)` ran.
The zero fast hit rate is `zero_fast / (zero_fast + zero_guard_fallback)`; a
zero denominator is unavailable, not a 0% hit rate. Floating normally has nil
base fee and therefore does not belong in that denominator.

The pool lock and the reheap mutex are both released before the single aggregate
record is published. Payloads use fixed arrays and are copied on recording and
snapshot, so later mutation cannot corrupt stored evidence.

Pages expose cumulative `reheap_recorded` and `reheap_overwritten`, counted under
the existing recorder lock. Overwritten counts include payload records evicted
by any later ring event. They are not per-reader loss. Use `lost_since_cursor`
and sequence continuity for capture loss; do not add cumulative overwrites to it.
`floor(rebuild/32)` supplies the expected per-pool sampled-round denominator.

## Validation and interpretation

The checked-in anonymous fixture retains real type/fee/tip/nonce distributions
for 39,168 transactions. Recipients, gas and payload layout are synthetic. Tests
use several input orders and cover exact sorting, membership, eviction,
insufficient-capacity rollback, stale heads, empty input, all comparator branches,
sampling, immutable snapshots and post-unlock publication (including state error).

Benchmarks compare the unchanged baseline, ordinary new rebuilds, forced sampled
rebuilds and the 1/32 cycle at 39,168 and 80,000 transactions. They include sample
setup and publication. The diagnostic queue scan uses the observed 473-account
scale; transaction layout and queue contents are synthetic. Fixed-seed inputs
do not reproduce live map iteration order. CPU profiles remain separate evidence;
CPU sample counts are not comparison counts, and wall time divided by comparisons
is not CPU time per comparison. Quantified local results are recorded separately.

## Local validation, 2026-10-25

The [machine-readable report](reheap-diagnostics-validation-20261025.json) binds
the baseline and implementation commits, fixture hash, test evidence hashes,
benchmark executable hashes, paired confidence intervals and a complete test
payload. Six package suites, targeted race tests and in-process RPC tests passed.
There are 58,800 direct comparator comparisons and nine frozen real-pool heap
scenarios, plus malformed caps, ties, eviction and failure branches.

Ten alternated-order paired runs on one pinned WSL CPU measured the following.
Time is median milliseconds per rebuild. Overheads are paired geometric means
against the baseline, not ratios of the displayed medians.

| Transactions | Baseline | Ordinary new | Sampled round | Exact 1/32 cycle, per rebuild |
| --- | ---: | ---: | ---: | ---: |
| 39,168 | 45.07 | 43.06 | 45.21 | 43.57 |
| 80,000 | 126.33 | 127.44 | 136.87 | 125.66 |

At 80,000 transactions a forced sampled round adds 10.60% versus baseline
(paired bootstrap 95% interval +3.59% to +19.63%). At 39,168 it adds 0.93%
(-3.46% to +5.03%). The 1/32 cycle measures -3.12% (-8.04% to +1.48%) and
-0.13% (-2.41% to +2.02%), respectively. These intervals do not establish a
speedup or zero overhead. The report also compares diagnostics with the ordinary
new implementation; the 80,000-transaction sampled increment is 11.15%
(+5.55% to +19.19%).

Ordinary retained event recording increases from 126.15 to 143.85 ns/call
(+13.80%, interval +12.60% to +15.02%), with zero allocations in both cases.
The existing sampling rejection path remains around 24 ns/call. Thus the patch
has a small fixed recorder cost outside sampled rebuilds as well. There is no
production event-rate extrapolation in these local results.

One full fixture test recorded 73,749 urgent-init comparisons, 220,864 urgent
pop comparisons and 7,832 floating-init comparisons. All 294,613 urgent effective
tip comparisons used the zero fast branch; the floating comparisons used nil
base fee. Counts vary with lookup iteration order. Its injected mutex wait and
phase durations are test data, not production measurements. No production
configuration, process or binary was changed during this validation.

## Production observation, 2026-10-25

Commit `870cc83` was fetched from main, tested and built on the server, then
started as PID `122797` with unchanged configuration. ELF SHA-256 is
`05b8a9f6ef3156cb8277f3cfb578455c64059515fc1d43ff3bdde6c3aa33f804`; `geth.prev` retains the preceding ELF.

The formal window is 2026-10-25T04:01:43.086344+00:00 to 2026-10-25T04:06:43.086344+00:00 (300 seconds), after the
declared 180-second peer/pool stability gate. There are 666 head
rebuilds and 21 expected/observed complete sampled rounds.

| Input transactions | Samples | Comparisons p50 | Zero fast rate | Rebuild p50 / p95 ms |
| --- | ---: | ---: | ---: | ---: |
| 38000-38999 | 21 | 295,832 | 100.00% | 39.124 / 51.770 |

Stage p50 times (ms) are: lookup 2.804, urgent_init 18.596, urgent_pop_split 16.646, floating_init 0.749.
The sampled head/operation/lock joins, stage counters and ring counter
accounting close; cursor loss is zero. Cumulative ring overwrite is retained
separately from reader loss. Per-round results, size bins, canonical head checks,
recovery details and artifact hashes are in the
[machine-readable release and observation record](reheap-production-20261025.json).

Urgent initialization and pop/split together account for 91.01% of summed
sampled rebuild wall time. Sampled `runReorg` hold p50 is
177.640 ms; demote p50 is 120.356 ms and pending-nonce rebuild
p50 is 12.881 ms. Reheap mutex waits are below the 1-us resolution
in all 21 samples. These are current-window decompositions, not a controlled
before/after performance result. The window only covers the 38k size bin.
