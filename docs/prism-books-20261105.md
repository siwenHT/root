# Prism post-state order books

`arb_getPrismBooks` reads `getOrderBookWithParams` on the BSC Prism router
`0xc2a6300eeaa828c8cf6e5906e65ea0f9b253c956`. It borrows a successful target or
auction post handle, resolves the engine's prepared child environment, and runs
StaticCall on a private state copy. It also accepts a pinned parent handle for
read-only verification. It never replays the target or writes canonical state.

The single object argument contains `request_id`, `post_handle`, `block_env`
(the existing prepared BlockEnv), `adapter`, `sender`, decimal `gas_price`,
`pairs` (`token0`, `token1`, sorted addresses), and `budget_us`. The quote context
passed to Prism is `(adapter,sender,gas_price,0)`. The router includes its taker
spread in each returned order-book level.

The response binds `boot`, `parent_hash` and `env_hash` to the borrowed state and
prepared child. `books` retains request order; each entry has raw ABI `data` or
an `error`. The Rust boundary decodes the ABI, checks pair identity, and applies
the bid/ask expiry before ranking. An exhausted read or time budget fails the
request. An empty valid book remains an empty book.

Validation: the real-EVM test `TestPrismBooksUsePostStateAndPreparedChild` reads
post-handle storage, child NUMBER and TIMESTAMP, checks that parent/post state
remain intact, and rejects a mismatched parent environment. The existing V2
reader and child-environment tests pass with this extension.
