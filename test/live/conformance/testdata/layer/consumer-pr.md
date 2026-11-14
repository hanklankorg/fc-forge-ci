<!-- SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Consumer PR application-test and lint enforcement

`TestForgejoLayer_ConsumerPullRequest` retains the clean case and adds
`application-test-findings` and `application-test-compile-error`, under the
existing visibility/object-format subtests. Both adverse cases commit real Go
test source, not a shell exit or a forged report. The offline fixture test runs
those sources through Go and the engine's Go JSON classifier.

The pinned `build-go` action always runs tests. Its existing administrator
renderer supplies `enforce-application-tests` from
`FORGE_CI_LIVE_ADMIN_APPLICATION_TESTS`. Run the same scenario with `false` and
`true` in separate authorized runs; do not override the value in consumer YAML.
Set `FORGE_CI_LIVE_ADMIN_APPLICATION_TESTS=false` or `true` in the run's
environment and select `TestForgejoLayer_ConsumerPullRequest` in both runs.
Direct focused runs can select
`^TestForgejoLayer_ConsumerPullRequest$/public$/sha1$/application-test-findings$`
or `application-test-compile-error`; the `clean` subtest is the positive control.

Expected outcomes:

| Source | Report-only | Enforcing |
|---|---|---|
| clean | build and downstream identity job succeed | same |
| assertion failure | recognized finding, binary verification and downstream job succeed | build fails, downstream job skipped |
| compiler error | not-waivable, build fails, downstream job skipped | build fails, downstream job skipped |

Every case retains the release-isolation job, exact first-attempt/job/runner
checks, unchanged PR and branch identities, and absence of tags, releases,
artifacts, deploy keys, secrets and new owner packages. A successful report-only
build also executes the existing version-stamped binary oracle. The native
assertions are maintained source, **not execution evidence** until separately
run with the frozen layer and its checksum-verified engine.

## Lint enforcement

`lint-findings` and `lint-tool-unavailable` run under the same subtests with
their own workflow, `consumer-pr-lint.yml`: the layer's `lint-nanolinter`
action in the build's place, before the same downstream identity job. The
consumer plans one check, `go-vet`, required and with `offline: 'true'`, so the
verdict needs no scanner database. The call passes no `enforce-*` setting; the
administrator renderer supplies `enforce-lint` and `enforce-vulnerabilities`
from `FORGE_CI_LIVE_ADMIN_LINT` and `FORGE_CI_LIVE_ADMIN_VULNERABILITIES`.

`lint-findings` adds `lint.go`, a self-assignment only `go vet` reports, and
runs in the nanolinter image of the Go flavour that
`FORGE_CI_LIVE_PR_LINT_JOB_IMAGE` names by digest. Without it the case skips;
the other cases need no such image. `lint-tool-unavailable` changes no source
and runs the gate in the build workflow's Go image, which holds no nanolinter.
Focused selectors end in `/lint-findings$` or `/lint-tool-unavailable$`.

| Case | `enforce-lint` false | `enforce-lint` true |
|---|---|---|
| finding | lint job and downstream job succeed; log holds go vet's line, `lint result=findings count=1 enforcement=false source=flag` and `vulnerabilities result=clean count=0` | lint job fails with the same lines at `enforcement=true`; downstream job skipped |
| gate cannot run | lint job fails on `nanolinter is not on PATH` with no policy receipt; downstream job skipped | same |

Both keep the no-publication checks above. The control is also the
vulnerability gate's on a pull request: one action and one engine call carry
both settings, and it fails under either value of each.

The offline tests vet the committed sources with the Go toolchain, check the
workflow's graph and call, and exercise the log oracle on good and bad logs.
Where the host has a `nanolinter`, they also run the sources through it and the
engine's gate; that nanolinter is the host's, not a job image's.

## Not covered

No vulnerability finding pair: every vulnerability check of the pull-request
gate (`osv`, `trivy-fs`, `go-vulncheck`, `rust-audit`) and the image scan of a
release or snapshot read a vulnerability database over the network, and no
finding is deterministic without one. This slice also does not test a release
publication job's refusal of adverse lint findings; Forgejo's release
workflows take no `enforce-lint`. GitLab/GitHub native work remains held;
Forgejo reproducible-build enforcement remains unconsumed and refused
(`TestLaneAdminInputsAreOnesTheForgeConsumes`). No provider or
release-wide enforcement coverage is implied by these PR cases.
