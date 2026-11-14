<!-- SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Live test ownership

- `internal/livetest` (relative to this directory): the code the live tests
  share, and the live tests that have no other home. `rawref` reads forge
  state straight from the forge's API, without the product's code, so a test
  can check the product against it. `probes` holds the scripts tests run
  inside forge jobs, and `testdata` the lab target files. Its commands:
  `preflight` checks a run's inputs before anything is built, `demoadmin`
  lists and deletes demos, `credentialproxy` limits a probe job to lab hosts,
  and `probejson` reads tokens and registry credentials inside a job, where
  there is no `jq`, and checks what they may write.
- `conformance`: shared provider scenarios and their layer fixtures.
- `githubconsumer`: hosted GitHub consumer scenarios.
- `fixtures/consumers`: consumer recipe projects.
- `fixtures/adapters/generic-cli`: actual-source adapters and their offline tests.
- `fixtures/oracles/maven-reactor`: downstream oracle of the reactor fixture.
- `fixtures/services`: installed-package oracle and inputs.
- `fixtures/development/rust-development`: four-role development receipt fixture.
- `fixtures/development/maven-reactor`: parent POM, library and application reactor.
- `fixtures/libraries/gradle-jvm`: Gradle library producer and its fresh downstream consumer build.
- `evidence.txt`: the hand-maintained evidence overlay of the testing matrices.

Ordinary `go test ./...` runs untagged support and fixture tests; the `live` tag
gates scenarios. To compile tagged owners without executing their TestMain or
scenarios, use `go test -tags=live -exec=/bin/true ./test/live/internal/livetest/... ./test/live/conformance ./test/live/githubconsumer ./test/live/fixtures/adapters/generic-cli/...`
from the repository root. This is compilation evidence, not live proof.

Live execution stays behind the guarded launcher in `scripts/ci`; see
[testing](../../docs/testing.md) and [live runs](../../docs/live-runs.md).
Production planners, adapters and CLI remain under `internal` and `cmd/forge-ci`.
