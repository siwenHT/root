# FluxPool prepared-state reader — 2026-11-18

`arb_getFluxPoolState` reads the qualified BSC USDT/WBNB pool without resimulating a target or quoting individual amounts. Entry is `0xff7fe6b6951afd81bc5ef9d205c28e5117012fd8`; pool ID is `0xa3b94efb3bde9749d7e2735e0dd4a4c9a7b502bb3c6c44ae30fd35b2563038ae`.

A request uses `parent_hash`, `budget_us`, and the existing `block_env`. For an existing simulated post-state it also supplies `request_id` and `post_handle`. The reader borrows that state, checks its parent, and resolves the exact prepared child environment. Results echo `boot`, `parent_hash`, `env_hash`, and `data`. A parent-only request with no environment is available for coarse screening; it increments the block number and inherits the parent timestamp. Final decisions must supply the full environment.

`data` is 21 ABI words: child block number; timestamp; current public 0x Settler from canonical `ownerOf(2)`; actual taker fee; Vault USDT/WBNB balances; Settler USDT/WBNB balances; readiness; Settler code hash; then the 11 raw pricing-pool storage words. Readiness checks Vault global/swap pause, its Entry binding, the Settler allowlist and both token allowlists. Withdrawal pause is independent. The reader checks the qualified pricing-router code hash and Entry/router/fee-manager bindings. The engine separately qualifies the Settler code hash before decoding its spending and payout semantics.

The fee read uses the router's authorized caller context through the existing budgeted static-call machinery. It does not modify production contract code, permissions or balances. All calls share the supplied parent/post state and child environment.

`eth/testdata/fluxpool-state.json` contains real BSC bytecode and storage at block 134276566. SHA-256: `9f9b5ba26c44a2afdc458431e937c8d4cc190538345a96b666ce2dd9436b884e`. Its generator is the engine repository's `protocol/v5/fluxpool-local-20261118/node_fixture.py`. Tests compare the full returned packet against that independent capture and exercise existing post-handle/environment identities, foreign-parent rejection and canonical-state isolation.

Validation: gofmt/goimports, real-contract FluxPool focused tests, LiquidFi/Lista/Prism/Elfomo regression tests, `make all` and `check_baddeps` passed. Full `ci.go test`, `lint` and `check_generate` were attempted on the final Go code and failed while downloading execution-spec fixtures, golangci-lint and protoc from GitHub (TCP timeout to `198.18.0.147:443`). They did not reach their respective checks and are not recorded as passing. Logs are under `D:/workSpace/arb_rust_archive/fluxpool-local-20261118/node-checks-final/`.

The user explicitly approved committing and merging this FluxPool change with focused verification in the current chat on 2026-11-18, while keeping the protocol off and without deployment. This is a task-specific exception to the AGENTS.md pre-commit checklist.

This task has not released or restarted the node. Live acceptance remains a later deployment step.
