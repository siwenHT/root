# LiquidFi canonical and post-target state

The pair 2 release uses the explicitly approved focused validation and live
health checks. The node baseline and the pair 2 candidate reported the same 12
repository-wide lint findings. The full run encountered a module checksum
download failure and the core suite's 10-minute timeout. Generated sources
matched in a native Linux checkout, while the module tidy check could not fetch
all dependencies. Focused LiquidFi and SquadSwap reads, the full command build,
and the dependency-boundary check passed on the release baseline.

`arb_getLiquidFiState` reads pair 1 (USDT/WBNB) or pair 2 (USDT/USDC) on entry
`0x114b11ae066a7785452c4570b42fff1b7fa5b001`. It verifies `pricing()` and
`vault()`, then reads the pricing implementation, packed price, capacities,
epoch inputs, same-block inputs, pause status and vault balances.

The argument requires `parent_hash` and `budget_us`. `pair_id` defaults to
1 for existing callers. With no `post_handle`,
the reader opens that canonical parent's state and uses parent number + 1.
With a `post_handle`, it also requires `request_id` and `block_env`, borrows
the existing simulated state and resolves that exact prepared child. It does
not execute the target again. Calls use the existing budgeted private state
and static EVM reader. The result binds `boot`, `parent_hash`, child `block`
and, for a handle, `env_hash`.

The price word is `keccak256(abi.encode(pairID, 0x8759f18911d145bf318ed8280c3612d855b1eb13a1ec427ab54ed53d55d77d05))`.
The block counter is `keccak256(abi.encode(childNumber, keccak256(abi.encode(priceWord,
0x8759f18911d145bf318ed8280c3612d855b1eb13a1ec427ab54ed53d55d77d0d))))`.
Its low/high uint128 fields are the two input directions. Rust checks the
verified pricing implementation and decodes these words at the RPC boundary.

`TestLiquidFiReadsThePostHandleAndChild` exercises the actual API and EVM
reader using a real test-chain post handle. It verifies storage, child number,
the block counter key, balance reads and parent isolation, and rejects a
mismatched requested parent or prepared child. The focused LiquidFi, Lista LP
and Prism reader tests pass under Linux Go 1.25. Windows cannot compile the
existing BLS CGO dependencies; the existing WSL toolchain was used.
