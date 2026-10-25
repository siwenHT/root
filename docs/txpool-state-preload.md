# Txpool state preload outside the write lock

Status: local implementation and validation, 2026-10-25. Not deployed. No
configuration, startup arguments or production process changes are part of this
work. Results and evidence hashes are in
[the validation report](txpool-state-preload-validation-20261025.json).

## Ownership and adoption

Only a direct-child reset with non-nil headers and a matching current pool head
is eligible. The existing single `runReorg` executor briefly takes `mu.RLock`,
copies/deduplicates address values from pending, queue and its notification map,
and retains identities of the old head, StateDB and noncer. Lists and mutable
state objects never escape that lock.

`StateAtForPool(root)` is an optional explicit ownership contract. Production
`core.BlockChain` returns a fresh StateDB using its existing `StateAt`. Providers
with only `StateAt`, including the shared-state legacy test double, retain the
old path. A wrapper must deliberately opt in; embedding the pool's `BlockChain`
interface does not silently inherit the capability. A provider violating the
contract is unsupported; identity guards additionally reject the old pool state
and its noncer fallback. Only underlying thread-safe Readers/databases are shared.

The executor calls `newNoncer(candidate)` **before the first account read**.
It then calls `GetNonce` once per distinct address on its exclusively owned
candidate. This loads the full account, including balance. Adoption installs
that candidate and its precreated noncer together at the original reset
publication point, under `mu.Lock`. It never copies a warmed StateDB.

Adoption rechecks old head hash, requested target hash/root and both old pointer
identities. `Clear` changes the noncer identity and invalidates the preparation.
No new generation counter is needed. Old references are released after the
check so slow heap rebuilds/subscribers do not prolong their lifetime. A queued
newer head does not invalidate the current request merely by becoming chain
latest: scheduler coalescing, done channels and shutdown retain their ordering.

Non-child resets, nil headers, unsupported providers, unavailable roots, failed
reads and invalidated views use the original state-opening/reset path. A failed
preparation is never installed as a successful zero-valued account read. A
transient warm-read failure can recover on the fresh fallback attempt; persistent
errors retain the original fallback behavior. This is not a change to general
StateDB error handling. Preparation may add work in failure/invalidation cases.

## Concurrent admissions and notifications

During preparation admissions still use the old view under the original pool
lock. Overlapping operations linearize before head installation; after Reset
completes no admission may use the old view. Accounts arriving after the address
snapshot are read normally by promotion/demotion at the target root. Nonces and
balances are not cached across roots.

All Forward/Filter, removal, demotion, reservation, gap repair, heap rebuilding,
nonce rebuilding and truncation remain in the write-locked flow. One necessary
notification guard was added there: queued notifications for transactions no
longer in `all` are removed. A barrier test reproduced a replacement accepted
during warming, removed by the new head, then emitted by the next promotion-only
round without this guard. The guard does not introduce a notification worker or
change the existing post-unlock feed boundary.

## Sampled evidence

An additive schema-2 `debug_pendingTiming` event has `scope=preload`,
`stage=state_preload`, `source=head_reset`. Its `operation` is the enclosing
`runReorg` lock operation; join by boot ID, pool and operation. `parent` is the
requested head hash; payload `root` is the requested root. Payloads are copied
on recording and snapshot and published only after the pool lock is released.

Eligible preparations sample the next rebuild number 32, 64, ... using the
existing per-pool counter. Ineligible reorgs/providers produce no preload
aggregate; do not treat absence as an adopted fast path. Version, rebuild and
sample denominator are explicit. Outcomes distinguish adopted, view changed,
old head changed, shared state, opening error and reading error.

The aggregate reports address snapshot wait/confirmed hold, state opening,
noncer creation, warming, attempted reader loads, partial read errors, tail
loads, write wait/confirmed hold and run-to-install/unlock/completion. The last
interval includes synchronous notification delivery but excludes scheduler
queueing before `runReorg` and its final done-channel close. Lock events remain
the authoritative precise held intervals. Reader attempts include absent
accounts/errors and are not physical IO. Tail loads include repeated misses for
nonexistent accounts; they are not a unique newly arrived account count.
On adoption the existing locked `state_open` phase measures handoff; actual
off-lock opening is reported in the preload aggregate's `state_open_us`.

Sampling adds no clock read, atomic counter or log per account. The existing
StateDB load/read counters are used at phase boundaries. Use page cursor loss
and sequence continuity for capture loss; there is no new independent ring or
per-reader cursor. A short capture can miss systematic samples.

## Verification and limits

Dedicated fresh-state tests assert that preparation and fallback branches really
execute. They compare pending/queue/all, reservations, virtual nonces, unique-price
heap membership/eviction and notification hashes. Cross-account map iteration is
not a total notification order. Tests cover nonce movement/rollback, balance
loss, gas limits, missing/zero-nonce accounts, replacement, SetCode, successive
roots, shared/nil/error providers and controlled Add/remove/Clear/head/Close
overlap. The production factory is separately tested for independent mutable
views with concurrent reads/writes. Existing reorg and pool regressions remain
applicable.

Benchmarks cover 37,275 pending accounts / 38,445 pending transactions plus 832
queue accounts / 1,973 queued transactions, and an 80,000-pending-account stress
case. Every measured round advances the header and creates a fresh StateDB.
Fees/transactions and account contents are synthetic. The hot model stores slim
account RLP bytes; the second uses a real committed trie in a memory database
with its clean-node cache disabled and a fresh trie reader each round. Neither
models physical disk coldness or replays production account state.

Ten alternating-order paired runs use five measured rounds per shape, one Go
toolchain and fixed CPU affinity. Both binaries use the identical final test
harness; a Go source overlay restores the original runtime for the baseline.
Whole-reset completion, actual write hold, CPU (Linux process ticks), allocation
bytes/count and GC are reported. CPU includes concurrent GC and has 10ms tick
quantization. Setup, trace extraction and benchmark logging are outside timed
and allocation intervals. Report per-round hold/completion percentiles as well
as paired run means. Predeclared budgets are 5% for total latency, CPU and
allocations; a shorter demote label alone is insufficient for acceptance.
