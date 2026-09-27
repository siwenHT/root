# Live sync header timeout

Observed on 2026-09-27 UTC: pending target
`0x0adcb40dc377e9a71c5016005f2f969aa074ab661711734428c9ce4e5ee0656e`
was built in 27–40 ms against a head already 52–130 seconds old. The node
subsequently recovered, then stalled again. Logs at 23:09:58, 23:10:58 and
23:11:58 show consecutive peers timing out at 60-second intervals. A preceding
failure explicitly reports `header request failed: timeout`. This does not prove
all stalls have the same cause; a blob-sidecar mismatch also occurred.

Blocking header requests now use the smaller of the adaptive network timeout
and `ARB_SYNC_HEADER_TIMEOUT_MS`. Default: 5000 ms. Accepted values: 1000–60000;
0 restores the previous adaptive timeout. Invalid values warn and use 5000.
The setting is read once when the downloader is created. Hash-based requests
also respond to downloader cancellation, as number-based requests already did.
Timeout warnings identify request kind, elapsed time and limit.

Body, receipt and state request timeouts are unchanged. This is not a pending
age filter and changes no consensus or block validation. Slow but valid peers
may be dropped sooner; monitor peer count and canonical head lag after rollout.

Validation: `go test ./eth/downloader -run 'TestHeader' -count=1`, followed by the
full downloader package tests and `go build ./cmd/geth`. Production acceptance
requires PID/ELF verification, RPC and WS recovery, database/WAL health and head
lag observations. Tests alone do not establish five successful pending landings.
