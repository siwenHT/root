# BSC v1.8.2 upstream merge (2026-11-15)

Release branch: `v1.8.2_merge`, explicitly requested for server deployment.
Fork baseline: `af0e24c` (`origin/main` at merge start).
Upstream: `bnb-chain/bsc` tag `v1.8.2`, commit
`54afaff4da31ac9e49ba646a4f65104edac5a2f3`.

The merge keeps the fork's arb RPCs, successful-pending simulation stream,
Flap approval prefixes, peer policies, timing diagnostics and read caches.
Conflicts use upstream consensus, synchronization and transaction-admission
changes, with the fork adapters updated for header-based state access, uint256
message amounts, GasPool cumulative accounting and EVM GasBudget calls.
`eth/ethconfig/gen_config.go` is regenerated from `config.go`.

The root-only arb capability probe remains an MPT state-reader probe; concrete
execution paths use the actual header through upstream `StateAt`.
Unsigned decimal amounts are bounded at the RPC decoding boundary before
conversion to the upstream uint256 message representation.

Regression fixtures follow upstream signed effective-tip ordering and use a
base-fee-valid transaction for the fork's simulated pending subscription.

## Validation and release boundary

Local WSL Go 1.25.0: `make all` passed. Tests for `eth`, `eth/arb`, fetcher,
filters, core transaction types and legacy pool, params, RPC, internal eth API,
and gethclient passed after adapter and fixture updates. Dependency hygiene
check passed.

Upstream's new full-repository pre-commit checklist reports pre-existing lint
issues. Full generation also changes unrelated upstream protobuf output with
the bundled generator. Unrelated generated changes are excluded. The complete
execution-spec fixture download did not finish during the release checks;
full-suite success is not claimed.

The user explicitly approved retaining existing check issues and proceeding
once merge regressions and the server build pass. Server release must fetch
this Git branch, test and compile there, validate the existing startup config,
retain only the previous runtime ELF as `geth.prev`, gracefully stop geth and
verify its new hash, progressing synchronized BSC head, RPCs, arb capabilities,
client reconnects and engine WAL/database/runtime counters. Old trading clients
must not be controlled. Server evidence directory:
`/home/bsc/codex_stage/node-v182-upgrade-20261115/`.
