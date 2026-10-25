# Pending notification timing

Sampled price-heap comparison counts and phase costs are described in
[reheap timing](reheap-timing.md). These additions have not been deployed by
the implementation/validation task.

Based on production source commit `a919c9a49290531f63b20564342a90e019a21ae2`.
This change only records evidence. The 200 ms announcement grace period, 50 ms
gather slack, txpool validation/promotion, 128-batch subscription channel,
24 simulation slots, result filtering and notification payloads are unchanged.
The production node restarted on 2026-10-23 at 19:34 China time; acceptance
completed at 19:41. The release
source was `3e2e7782edb4e3a7fdd4cd8896f8b072f089bac9`; the server-built ELF
SHA256 is
`6d8c976399c6e4baab54a2b4a76e2141ae724460a87628c22a5de3b5dc8fb6bf`.
Configuration and timing/concurrency settings were unchanged. See the dated
[release acceptance record](pending-timing-release-20261023.json) for process,
reconnection, rollback and sampling evidence. This is a historical release
record; query the live process to establish its current version.
Pre-release review and build-identity verification:
[2026-10-23 review](pending-timing-preflight-20261023.md).

## Lock and packet extension (2026-10-24)

The extension implements the follow-up plan from the read-only lock/P2P
investigation. It changes diagnostic metadata only; it does not change admission,
lock order, the announcement grace period, rejection backoff, channel capacities,
simulation concurrency, wire encoding or notification payloads.

Source `c45f3746f807e23d3981515ef5983ceb980fec95` was built from server
`main` and deployed on 2026-10-24 at 21:00 China time. Acceptance completed at
21:10 with configuration unchanged. The server-built and running ELF SHA256 is
`a55718575e8a03f09137883fbdf916f45ef55705741b1f964ba82f0aec055074`.
See the dated [release and observation record](pending-lock-p2p-release-20261024.json).
This records that release, rather than establishing a future live version.
The authorized 21:11:12-21:16:12 China-time capture retained a continuous event
sequence, including a 10-second prebuffer and 45-second drain. See the
[five-minute findings](pending-lock-p2p-observation-20261024.md) for the demote /
price-heap lock breakdown, explicitly linked local P2P waits, rare rejection
backoffs and the remaining uncertainty before the earliest retained read.

Pages now report `schema_version=2`. Existing hash events remain sampled at 1/32.
Records with `scope=lock` or `scope=packet` have a zero hash and **are not
transactions**. They use the same bounded ring, sequence and loss counters, so
collectors must page often enough for the higher event rate. The analyzer
excludes scoped records from the transaction denominator.

All production acquisitions of the legacy pool RWMutex record `pool_lock_wait`
and `pool_lock_hold`, including readers and Add calls without a sampled hash.
`pool`, `operation`, `source` and `outcome=reader/writer` identify each holder.
The hold endpoint is captured immediately **before** Unlock; publication is
after Unlock. These are confirmed held intervals, not exact mutex grant times.
Uncovered waiting time remains unknown, including timestamp resolution and
retention gaps. Concurrent read-holder overlaps are unioned, never summed.

`runReorg` records `pool_phase` spans for `reset`, `state_open`, `promote`,
`demote`, `priced_reheap`, `pending_nonce_rebuild`, `truncate_pending` and
`truncate_queue`. State opening is nested in reset. Head-reset-only phases are
absent on promotion-only runs. All phase publication happens after unlock, even
when opening state fails. Stage counts do not represent time shares.

`connection` is an opaque boot-local ID; `packet_number` increments across all
messages on that connection. `packet` is a unique operation ID and
`previous_packet` links the read loop. `protocol`, `protocol_version` and
`message_code` describe the **negotiated relative** code, avoiding assumptions
about the BSC/ETH protocol offsets. This metadata follows the message through
hash/body decoding, fetcher callbacks, aggregate admission and legacy admission.
`parent_operation` links legacy admission to its aggregate Add operation. On a
`p2p_handoff_previous_handler` span it instead identifies the previous packet delivered to
that protocol handler.

Packets are retained when a sampled hash selects them, every 32nd message on a
connection, on read/handler/close/cancellation failures, on channel handoffs
taking at least 1 ms, and on the existing 200 ms rejection-backoff branch. Slow
P2P handoffs also retain the preceding protocol-handler packet. The four
handoff/handler lifecycle stamps are bounded and synchronized; selecting a
packet after a concurrent completion neither duplicates nor loses the stamps.
Only the last delivered packet is retained per protocol; there is no peer
history or payload retention.

Packet evidence separates:

- `p2p_read_gap`: the interval after the previous ReadMsg returned and before
  this ReadMsg started, including dispatch, scheduling and diagnostic overhead.
- `p2p_previous_dispatch`: the preceding message's dispatch, nested in that gap.
- `p2p_read`, `rlpx_io_window`, `rlpx_frame_ready`, `rlpx_decode_done`: transport
  reading, the window from first underlying read to last underlying completion,
  authenticated/decrypted frame completion, and code/snappy decoding. The I/O
  window can include computation between reads; it is not remote propagation.
- `rlpx_prefetch_wait`: previously read bytes waiting before the next ReadMsg.
  `buffered_bytes >= frame_bytes` with `outcome=complete_frame` proves the whole
  frame was already in the user-space buffer. `BufferedAt` uses the latest read
  completion contributing to the buffer, so its duration is a conservative
  local-wait lower bound. A partial buffer does not prove a whole transaction
  had arrived. No new socket read is fabricated for a fully prefetched frame.
- `p2p_handoff_begin/end`, `eth_handler_begin/end`: protocol handoff and handler
  occupancy, with explicit failure/close outcomes.
- `fetch_admission`, `fetch_rejection_backoff`: a unique fetcher batch ID, batch
  size, rejected count and the existing rejection bucket `other_reject`.
  Rejection sleep is separate from `fetch_wait_begin/end` announcement grace.
- `fetch_notify_handoff`, `fetch_cleanup_handoff`: fetcher channel waiting,
  including cancellation and protocol-violation outcomes.

The offline report's `pipeline` section contains packet links, pool phases and
retained holder intersections. Explicit packet links improve repeated-hash
matching, but missing/ambiguous ingress and any retention gap remain unknown.
Gap, dispatch, handoff, prefetch and lock intervals can overlap. The report does
not add them into an end-to-end delay or rename unexplained time as remote
propagation. Kernel receive timestamps and peer send times are not measured;
an empty kernel queue would also not exclude user-space prefetch.

Implementation validation uses Linux Go 1.25.0 with CGO, including the affected
packages below plus `./p2p/rlpx`, `./p2p` and `./eth`. New tests exercise real
RWMutex contention and delayed/failed StateAt, encrypted-frame prefetch, a
blocked protocol preventing the next transport read, closed/cancelled/error
branches, unchanged RLP bytes and the actual 200 ms rejection sleep. Python
tests include read-holder unions, missing owners, packet isolation and gaps.
Run them with `python3 -B -m unittest discover -s deploy -p 'test_pending*.py'`.

## What the boundaries mean

| Question | Boundaries / evidence |
| --- | --- |
| When did this node first receive a hash or full body? | `hash_received` / `body_received` reuse `p2p.Msg.Time()`, before lazy per-transaction decoding. `body_decoded` reuses each decoded transaction's time. These are local receive times, not the peer's send time. A missing receive time stays missing. |
| Did the fetcher consume the announcement grace period? | `fetch_announce`, `fetch_wait_begin/end`, `fetch_request_queued/begin/end`. Wait events include the actual fetcher's configured `wait_us` and `slack_us`; matching wait boundaries share an operation ID. The request call's return means request dispatch returned, not that the body arrived. |
| Was the transaction blocked during admission? | `pool_add_begin/end` span the aggregate pool call, including rejected results. `legacy_add_begin/end`, `legacy_lock_wait/acquired/released` and `legacy_promote_begin/scheduled` separate initial validation, mutex wait, time holding the lock and promotion-scheduler handoff. Mutex evidence is published after unlocking. |
| Did the event or subscription channel queue it? | `feed_emit_begin/end`, `feed_received`, `subscriber_send_begin/end`, `subscriber_batch_dequeued`. A blocking send and buffered-channel residence can overlap; send return is not an exact enqueue timestamp. |
| Was it waiting for a simulation slot or a goroutine? | `simulation_batch_dequeued`, `simulation_scheduled`, `simulation_started`, `simulation_finished`, `simulation_result_dequeued`. Batch dequeue to worker start includes slot/scheduling wait. Scheduled to started isolates the goroutine-pool part. Finished to result-dequeued is a separate result-queue delay. |
| Which state did simulation actually select? | `simulation_state_ready` records the backend-selected header's parent and `miner_pending` / `canonical_head`, rather than assuming the earlier batch header was used. Preparation is included in total simulation time. A pending miner state can already contain transactions; this marker does not claim a pristine canonical-parent snapshot. |
| When was the notification written? | `payload_build_begin`, `notify_begin`, `notify_lock_acquired`, `notify_return`, `codec_write_begin/end`. The codec span includes encoder-lock wait, JSON encoding and transport output. Its end is the transport call's return, not Rust receipt. Inactive subscriptions retain the attempt in the activation buffer; `notify_buffered` is not a write. |

`tracker_first_seen` retains the existing acceptance-tracker meaning and original
millisecond RPC value; it is recorded **after** pool admission, not at earliest
network receipt. Promotion/feed/simulation are asynchronous and can precede it.
Parent-block age must not be interpreted as transaction transmission time.

A direct body broadcast can bypass the hash-only wait. Blob announcements also
bypass its grace period. Bodies may arrive while admission blocks and before
the fetcher's cleanup runs, so compare body receipt with the wait/request
boundaries rather than assigning every transaction 200 ms. Repeated announces,
request attempts and concurrent duplicate admissions remain separate evidence.
Legacy-pool lock spans cover the legacy pool; other pool types still have the
aggregate admission span and must not inherit legacy mutex measurements.

## Correlation, retention and access

The process samples hashes deterministically at 1/32 at every boundary. The
fixed ring holds 65,536 **boundary events**, with no payload, calldata, raw
transaction, peer address, credential, disk writes or logging dependency.
Sampling and ring capacity have no runtime configuration knobs.

Each event contains a hash, microsecond wall time and process-relative monotonic
time. `boot_id` identifies the process. Simulation records additionally identify
the internal EventSystem subscription, client-visible RPC subscription, unique
simulation attempt, batch and actual parent when preparation succeeds. Operation
IDs pair the relevant admission/request/feed/dispatch boundaries; they are not
a promise of one linear sequence across concurrent operations. A new node
simulation attempt is distinct from Rust's existing next-head retry.

The read-only RPC method is **`debug_pendingTiming`**, arguments
`[after_sequence_hex, optional_page_limit_hex]`, for example `["0x0","0x400"]`.
It returns at most 1,024 events. Use the returned numeric `next_sequence` as the
next cursor, converted to hexadecimal for the RPC argument. `oldest_sequence`,
`latest_sequence`, `overwritten` and `lost_since_cursor` expose retention gaps.
Reset the cursor if `boot_id` changes. Reading never clears or changes the ring.
It uses the existing debug namespace; there is no new listener or subscription.

`overwritten` is cumulative for this boot, including events before a capture.
`lost_since_cursor` counts unavailable events after the supplied cursor; repeated
reads can report the same loss. Neither is a transaction-loss count. The analyzer
reports the maximum reported cursor loss and gaps between unique saved sequences
separately, without summing overlapping pages or claiming a total capture loss.

Sequence orders insertion, **not event time**: pool-lock/feed boundaries can be
published after the operation. Use monotonic time for same-process durations,
and wall time only to join the existing Rust `received` evidence. Collect failed,
reverted, skipped and cancelled records as well as successful notifications.

For a steady-state diagnosis, a five-minute capture can page the RPC
through the existing IPC connection and save complete responses as JSONL. Start
from a recorded cursor, drain outstanding attempts, retain all gap counters and
join hashes to Rust receipt/first-vs-retry evidence. No such production capture
was performed during implementation. The authorized production rollout later
collected a separate 30-second acceptance sample; it does not establish a
steady-state bottleneck.

Analyze saved pages locally:

```sh
python3 -B deploy/analyze-pending-timing.py saved-pages.jsonl --output timing-report.json
```

The tool makes no network calls. It deduplicates page overlap by boot/sequence,
keeps admission operations, subscriptions and attempts separate, and reports
known-span sample counts, p50/p95/max. Unique ingress chains also expose wait-end
to request allocation, request-to-body time (request replies only), and body to
pool-call entry; broadcast arrivals are not relabelled as request round trips.
Missing, negative or ambiguous spans are
null, never zero. Pre-batch delay is computed only for a unique subscription
delivery with retained ingress/firstSeen evidence and no reported gaps;
repeated feeds remain explicitly unknown. Overlapping spans must not be summed.
Absence of a write or simulation-state boundary is not evidence of zero delay.
Known retention gaps also make cross-operation ingress matches unknown, since
an evicted duplicate could make the remaining boundaries look unique. Retained
endpoints sharing an operation/attempt ID can still establish their own span.

## Local validation

Use Go 1.25.0 on Linux with CGO (the existing BLS dependencies require it):

```sh
go test ./common/pendingtrace ./core/txpool ./core/txpool/legacypool ./eth/fetcher ./eth/filters ./rpc ./eth/protocols/eth ./internal/ethapi
go test -race ./common/pendingtrace ./core/txpool/legacypool ./eth/fetcher ./eth/filters ./rpc ./eth/protocols/eth ./internal/ethapi -run 'TestPendingTiming|TestPendingPipeline|TestPendingArrival|TestSamplingAndMissing|TestBoundedPagination|TestConcurrentRing|TestAttemptCorrelation'
python3 -B -m unittest discover -s deploy -p test_pending_timing.py
go build ./cmd/geth
```

Tests exercise the real fetcher with a simulated 200 ms grace period, actual
LegacyPool mutex contention, a saturated simulation pipeline with a queued
second batch, independent subscriptions, simulation reattempts/reverts/cancels,
activation buffering, blocked/failed writes, unchanged RLP/JSON payloads, a
bounded concurrent ring, non-destructive paginated RPC reads and loss/ambiguity
handling in the offline analyzer. These prove instrumentation behavior, not a
production latency improvement or a measured production bottleneck.
