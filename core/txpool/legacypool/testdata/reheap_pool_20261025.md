# Anonymous fee-priority snapshot

Source: read-only production `txpool_content`, 2026-10-25 around 01:00 UTC,
node `4d600135dd785cfd2c20ed48a56d7043fbfd5edd`.

The gzip fixture retains only pending/queued group, transaction type, nonce,
fee cap and tip cap. It contains 39,168 records: Legacy 34,241, DynamicFee 4,575,
AccessList 340 and SetCode 12. No addresses, hashes, calldata or signatures.

SHA256: `1aa8003cfce0934b1281b9f115690e0cf6aaa1b945fc96a6fcb8009c2b78e306`.

Tests reconstruct distinct synthetic recipients with gas 21,000 and no payload.
This preserves fee/type/nonce distributions, not live map order or object layout.
Seeds vary initial order. The 80,000-record case repeats the observed distribution.
