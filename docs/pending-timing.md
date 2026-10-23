# Pending notification timing (local implementation, not deployed)

Based on production source commit `a919c9a49290531f63b20564342a90e019a21ae2`.
This change only records evidence. The 200 ms announcement grace period, 50 ms
gather slack, txpool validation/promotion, 128-batch subscription channel,
24 simulation slots, result filtering and notification payloads are unchanged.
No production configuration or running process has been changed.
Pre-release review and build-identity verification:
[2026-10-23 review](pending-timing-preflight-20261023.md).

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

After a separately authorized release, a five-minute capture can page the RPC
through the existing IPC connection and save complete responses as JSONL. Start
from a recorded cursor, drain outstanding attempts, retain all gap counters and
join hashes to Rust receipt/first-vs-retry evidence. No such production capture
or release was performed for this implementation.

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
