# Pending timing pre-release review — 2026-10-23

Scope: local review, builds and tests. No push, deployment, production
configuration changes, new production capture or production process restart.
The reviewed implementation is
`af39d6d219c7d56b139b6a140065aefbda479d1d`, tree
`aa3e7986b33f4cbe36b8bbb93de11826ee986adf`.

## Original build identity

A new Linux amd64 / Go 1.25.0 / CGO ELF was built from an unchanged Git checkout
of that exact commit. Both `geth version` and `go version -m` identify the full
commit; the latter also reports `vcs.modified=false` and commit time
`2026-10-23T09:51:39Z`.

SHA256:
`31579eed77dd2a004014ec04e5244f17220ee92e0d386f72e819d61f775bf00d`.

The previous validation ELF, SHA256
`4bfcf439cd8ec6c351016775917a635a628332e50dc3f3ed85fefea9483f21e5`,
did not embed a revision and remains an earlier validation artifact. Its identity
must not be substituted for the new build.

The installed Go 1.25.0 VCS detector checks for a `.git` **directory** and does
not recognize a worktree's `.git` file, even with `-buildvcs=true`. Builds used
a separate local Git checkout containing a real `.git` directory. The existing
project linker variables were also set from the checked-out revision/date:

```sh
commit=$(git rev-parse HEAD)
date=$(git show -s --format=%cs HEAD | tr -d -)
test -z "$(git status --porcelain)"
go build -p 4 -mod=readonly -trimpath -buildvcs=true \
  -ldflags "-X github.com/ethereum/go-ethereum/internal/version.gitCommit=$commit -X github.com/ethereum/go-ethereum/internal/version.gitDate=$date" \
  -o "$output" ./cmd/geth
go version -m "$output"
"$output" version
sha256sum "$output"
```

Build output stays outside the source checkout. Check source cleanliness again
after building. A future production build must bind its own main commit and ELF
hash; this local build does not identify a future server-built ELF.

## Findings and corrections

1. **Rejected entries inherited mutex evidence in a mixed legacy-pool batch.**
   `af39d6d` recorded lock/promotion boundaries for all sampled batch entries,
   including entries excluded before locking. The regression uses a real pool,
   one unsigned rejected transaction and one valid signed transaction under
   mutex contention. It failed before the correction. Only entries reaching
   `addTxsLocked` now receive lock/promotion boundaries, retaining their original
   operation ID; all entries retain admission begin/end. Admission behavior and
   returned errors are unchanged.
2. **Retention gaps could falsely establish unique ingress matches.**
   The original analyzer could join a body to an admission across separate
   operations despite a reported cursor loss or saved sequence gap. An evicted
   duplicate could invalidate that uniqueness. Both regressions failed before
   the correction. Cross-operation ingress matches now remain null under known
   gaps; retained matching operation/attempt endpoints remain measurable.

The commit containing this review carries these corrections and additional
failure-branch tests. The original `af39d6d` has a verified build identity but
should be superseded by the corrected candidate before a release.

## Correlation, loss and failure audit

| Area | Verified contract |
| --- | --- |
| Correlation | Join within boot/hash, then operation kind + operation ID, or internal subscription + simulation attempt. RPC subscription IDs follow sampled attempts through activation buffering. Reattempts and independent subscriptions remain separate. |
| Actual parent | `simulation_state_ready` uses the backend-selected header parent and state source. Preparation failure leaves this boundary absent. A missing marker does not establish zero preparation time. |
| Sequence | Sequence orders publication, not event time. Pool-lock/feed markers published later retain their captured monotonic timestamps. Overlapping asynchronous spans are not added together. |
| Loss counters | `overwritten` is cumulative per boot. `lost_since_cursor` is relative to each request. Multi-wrap, repeated-read and drain tests distinguish the counters. Sampling excludes 31/32 hashes intentionally and is not counted as lost evidence. |
| Analyzer loss | Duplicate pages are deduplicated by boot/sequence; conflicts and RPC errors fail. Maximum reported cursor loss and internal unique-sequence gaps stay separate; they do not claim a total loss count. |
| Fetch/admission failure | Request dispatch errors retain a matching operation and error outcome. Aggregate admission preserves accepted, rejected and unsupported-type errors in the original order. Pre-validation rejection has no lock/promotion span. |
| Simulation failure | Error, nil receipt and revert remain separate outcomes and do not reach notification. Running cancellation retains an attempt; unscheduled current-batch cancellation retains the batch without inventing an attempt. |
| Notification failure | Inactive buffering is not a write. Blocked output has no completion marker until return. Write errors retain error outcomes and the original returned error; diagnostic cancellation does not alter transport cancellation. |

The live subscription's existing behavior is preserved: notifications still
require a successful simulation; its seen/inflight rules and error handling
are unchanged. Queued upstream batches without a dequeue/attempt are not
claimed as fully traced cancellations. Malformed protocol packets rejected
before normal handling are outside the accepted-pending timing chain.

The 200 ms announcement grace, 50 ms slack, txpool acceptance/promotion rules,
128-batch channel, 24 simulation slots, notification payloads and Rust retry
policy are unchanged. This audit does not establish a production bottleneck or
measure total node overhead. Production capture remains a separate next step.

Local verification passed: 310 test cases including subtests across eight full
Go packages, 25 focused race cases across eight packages, and eight offline
analyzer tests. The two regression cases were first observed failing on the
original implementation. Build manifests, raw test output and ELF hashes are
retained outside the repository in the local preflight evidence directory.
