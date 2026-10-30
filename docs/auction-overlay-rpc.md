# 48Club storage-only pending source (2026-10-30)

Three additive methods reuse the existing pinned-parent workers, environment,
EVM, pool readers and executor ledger. Canonical state and normal pending RPCs
are unchanged.

* `arb_prepareAuction`: `request_id`, `stamp`, `parent_handle`, `budget_us` (number),
  `auction`; returns a job of kind `auction_overlay` with a post handle.
* `arb_simulateAuctionCandidate`: the normal candidate arguments plus `auction`;
  no target raw; result kind `auction_candidate`.
* `arb_simulateAuctionRaw`: the schema 4 state-raw arguments plus `auction`;
  exactly our signed transaction; result kind `auction_raw`. Success requires
  the same executor ledger, actual nonce, gas and balance rules as state-raw.

`auction` contains `id`, `content_digest` (lowercase hash32) and `states`
(lowercase address -> hash32 slot -> hash32 value). Slots are applied as a diff,
including explicit zeros, on an isolated parent copy. Omitted slots, code,
balances and account nonces are preserved. No target transaction is synthesized.
The caller must establish data completeness and freshness; this API does not
claim the feed provides the complete result of an independently replayed target.

The overlay digest is the standard length-prefixed `arb.auction.overlay.v1`
digest of ID, content digest, u32 account count, then sorted addresses, u32 slot
counts and sorted slot/value pairs. The candidate/raw request digests wrap their
existing digest and the overlay digest under `arb.req.auction.v1`, in separate
job namespaces. Prepare uses `arb.req.prepareAuction.v1` and the ASCII wire
parent handle/stamp plus binary overlay digest. The signed result additionally
echoes `auction_id` and `overlay_digest` beside the raw/environment identities.

Validation: `go test ./eth -run TestAuction -count=1`, plus existing prefix,
candidate and post-state tests. The Rust client shares a frozen digest vector.
These tests prove local overlay behavior, not live feed accuracy or profitability.
