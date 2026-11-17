# Ellipsis state reader validation

2026-11-18. Adds `arb_getEllipsisState` for canonical and isolated post-target states, with physical base-pool dependencies, child timestamp, old metapool LP-rate cache, and Valas reserve availability/configuration.

The reader uses BSC v1.8.2 `StateAt(header)`. Real-contract fixture tests cover all five pools and an isolated post handle; parent identity, target time and original state isolation are checked.

Checks completed: gofmt, goimports, all-command `make all`, focused LiquidFi/Ellipsis tests, and `check_baddeps`. Full tests, lint and generated-code checking were attempted but blocked while downloading external fixtures/tools. These are not recorded as passed. The user explicitly authorized recording these limitations and merging/releasing with focused validation on 2026-11-18.

Raw local evidence: `D:/workSpace/arb_rust_archive/ellipsis-local-20261118/node-validation.json` and the adjacent node check logs. Production rollout evidence is tracked in the arb-engine repository's Ellipsis release record. No binaries or secrets are committed.
