# Price-heap workload, 2026-10-24

`priced_pool_20261024.json` contains only aggregated public gas-price counts from
one read-only `txpool_inspect` snapshot of node source
`c45f3746f807e23d3981515ef5983ceb980fec95`: 38,292 pending and 582 queued
transactions. Equal price buckets from the two sections are combined and sorted.
No addresses, signatures, payloads or credentials are included.

`BenchmarkPricedReheapCapturedPool` expands all 38,874 observed prices. The 80,000
case repeats that corpus before shuffling with seed 24. It measures the unchanged
production `Reheap`, including map iteration, allocations, full heap construction
and the 20% floating-heap split. Parsing, constructing transactions, caching hashes
and the initial heap are outside the timed loop.

Transaction types (all Legacy, or 50% Legacy / 50% DynamicFee), unique nonces and
recipients are synthetic. Each observed price supplies both fee cap and tip cap.
The snapshot did not record their joint distribution or transaction types, so this
is a captured price/size workload, not a replay of signed transactions. Account
counts do not affect `Reheap`, which reads the transaction lookup directly.

Use the same fixture, Go version, build tags, hardware and benchmark options for
the baseline and patch. Run them alternately with at least ten samples. Compare
the complete `Reheap` time and allocations, alongside the comparator benchmarks
(including nonzero base fees). A microbenchmark gain alone is not acceptance.
