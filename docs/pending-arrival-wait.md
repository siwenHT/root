# Pending announcement grace period

The node waits 200 ms after a non-blob transaction hash announcement before
requesting its body, allowing ordinary broadcasts to arrive first. On 2026-09-28,
a 20-second read-only probe matched announcements to 2027 accepted transactions;
397 took at least 100 ms. Only the first 16 hashes per announcement were sampled.
These were general transactions, not a profitable-target cohort. A separate
sample saw 42 accepted request replies and 2216 accepted broadcasts.

`ARB_TX_ARRIVE_TIMEOUT_MS` configures this grace period once per fetcher. Default
200 ms, allowed 1–2000 ms; invalid values warn and fall back to 200. Arrival
coalescing slack is the smaller of 50 ms and one quarter of the configured wait.
The existing five-second request timeout and its slack remain unchanged. Blob
transactions still bypass the announcement wait. No validation, peer request
limit, or pending age filtering changes.

The Git-managed `deploy/run-node-pending-fast.sh` starts the existing z_run.sh
with a 25 ms wait unless the environment overrides it. It preserves existing
arguments and the canonical geth executable. Launch this wrapper for the trial;
the startup log records the applied wait/slack. More requests may be sent before
broadcasts arrive, increasing network load; there is no promised 175 ms saving
for transactions that would have arrived by broadcast sooner.

Validate with `go test ./eth/fetcher -count=1`, including simulated-clock tests
for the 25 ms deadline, followed by a geth build. After Git deployment and a
rollback-ready node restart, verify PID/hash, environment, advancing canonical
head, peers, engine reconnection and WAL/PG counters. Repeat announcement-delay
sampling and compare actual target processing/submission times. A faster feed
is not proof of the goal of five profitable pending backruns landing.
