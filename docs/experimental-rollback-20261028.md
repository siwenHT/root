# Experimental node code rollback, 2026-10-28

The user reported increased node latency and requested removal of experimental code. The running geth is SHA-256 `05b8a9f6ef3156cb8277f3cfb578455c64059515fc1d43ff3bdde6c3aa33f804`, embedded clean source revision `870cc83debe3c0aa8e810bad038c00b050015afb`. Its predecessor, `2925fc512125d97704c3603a7d6d6150e19df3cdefb0913ad83a0db46b33f031`, embeds `4d600135dd785cfd2c20ed48a56d7043fbfd5edd` and still contains experiments; swapping the existing geth.prev would not remove them.

All 68 non-documentation files changed since `a919c9a49290531f63b20564342a90e019a21ae2` are restored exactly to that baseline in a new Git commit. This removes sampled pending/lock/P2P/reheap instrumentation and its debug RPC, the zero-base-fee comparator experiment, and the later local-only state-preload implementation and fixtures. State preload was never part of the running `870cc83` binary and cannot explain its latency. Historical reports are retained as dated evidence.

The arb APIs, protocol readers, execution validation, configured post-pool window, fetcher wait defaults, databases and production configuration remain at the baseline behavior. No startup arguments or tuning settings change. In particular, this task does not disable the existing canonical V3 state cache or change the 200ms announcement wait. Runtime files outside docs must have an empty diff against the baseline before committing.

The server must pull main, test the restored packages and arb interfaces, build geth and validate the existing config using dumpconfig. Before restarting, record current process/ELF/configuration identities, node sync/head/peers, engine health, database and WAL counters; retain the current ELF as the sole geth.prev. Stop only the verified node PID gracefully, then launch the canonical /home/bsc/fullnode/geth with the existing startup script. Do not copy node state or engine WAL; do not signal old client processes.

Acceptance requires a new node PID matching the built ELF, progressing synchronized BSC head, IPC/HTTP/WS and arb capabilities, and automatic client reconnection. Header-to-local delivery includes propagation/import/notification and cannot isolate CPU time or establish causality from an uncontrolled before/after window.

## Deployed and verified

Server main `8bfb63116798f07ca453a8eb14276a277b920573` passed 784 tests across 16 packages with the original Go 1.25 toolchain. Build and unchanged production dumpconfig checks passed. An initial Go 1.24 invocation was rejected before testing; that environment failure remains in the evidence logs.

The verified old node PID 122797 received SIGINT at 2026-10-27T17:32:02Z and shut down in 2.501 seconds. New PID 2947994 runs ELF `265b9c235b37f72956b1a6c9602344aedb75557f66626300c75c3e470ee618f3`; basic RPC readiness took 21 seconds. The old running ELF is the sole geth.prev. Startup arguments, node/engine configuration and engine ELF are unchanged; no database or WAL copies were made.

Recovery acceptance passed at 17:40:36Z: synchronized progressing BSC head, recovered peers, working IPC/HTTP/WS and schema-4 arb capabilities, diagnostic RPC absent, connected/unpaused engine with advancing counters, monotonic database counts and preserved nonempty WAL paths. rust1/rust2 identities did not change. rust5 PID changed from 3489343 to 2953791 during recovery; no task command controlled an old client. All clients reconnected.

Two fixed 60-second windows each captured 134 heads and 33 HTTP RPC probes with no errors. Head delivery P50 was 106.663 -> 110.034ms and P95 was 218.501 -> 229.702ms; HTTP RPC P95 was 1.522 -> 0.850ms. **Block-delivery latency did not improve in these samples.** Peer sets, txpool size and cache warmth differ, so lower CPU usage is not a controlled causal result. This rollback does not establish why the reported latency increased.

The [validation summary](experimental-rollback-20261028.json) records checks and measurement bounds. Full public evidence, checksums and logs are under `/home/bsc/codex_stage/node-experiment-rollback-20261028/`; the Rust repository's `protocol/v5/node-experiment-rollback-20261028/README.md` is the shared operations entry point.
