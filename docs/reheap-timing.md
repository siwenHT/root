# Sampled price-heap rebuild diagnostics

These diagnostics add no configuration or RPC endpoint and do not change heap
policy, comparison results, the 4:1 heap ratio or rebuild frequency.

Only head-reset rebuilds are eligible. Under the existing pool lock a per-pool
counter advances for each actual rebuild, including an empty pool. Rounds 32,
64, ... collect a complete sample. Promotion-only calls and missing new heads
do not advance it. The requested new head is recorded; join `state_open` by
parent operation to identify a failed state reset rather than assuming it worked.

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
