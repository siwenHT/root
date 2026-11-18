# ElfomoFi prepared-state reader — 2026-11-18

`arb_getElfomoBook` reads the fixed BSC ETH/USDT helper through the existing budgeted static-call reader. Both canonical and existing post-handle calls resolve the exact prepared child environment and return boot, parent and environment identities. It does not resimulate the target.

Real helper bytecode/storage fixture tests and the LiquidFi/Ellipsis regression readers passed. `make all`, gofmt/goimports and `check_baddeps` passed. Full `ci.go test`, `lint` and `check_generate` were attempted but blocked by GitHub download timeouts for execution-spec fixtures, golangci-lint and protoc-gen-go. An alternate read-only download path was tried; it did not complete the required checks. These checks are not recorded as passing.

The user explicitly approved proceeding with focused verification and post-release health acceptance for this change in the current chat on 2026-11-18. This is the documented exception to the pre-commit checklist in AGENTS.md, not a relaxation for future changes.

Live release acceptance must verify the main-derived ELF, canonical and existing-post reads, advancing head, engine connections and decision counters. The reader does not prove the SDK model exact: previously observed 1 wei interpolation differences are explicitly accepted by the user and the engine retains final EVM profit checks.
