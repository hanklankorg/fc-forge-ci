<!-- SPDX-FileCopyrightText: The Forge CI Authors -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Maven service development acceptance

The existing `TestForgejoLayer_ConsumerDevelopment` selects the Java-service
case when `FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE` is set. This is a public,
native **linux/amd64**, unsigned snapshot trial, using the existing Consumer
private-index staging, scratch-resource cleanup and shared snapshot workflow.
It does not exercise stable release, PR linting or other forges.

## Operator inputs

Use the normal live-suite contract/session and strict base UID setup. Select:

| Environment variable | Value |
| --- | --- |
| `FORGE_CI_LIVE_CONSUMER` | Absolute path to a clean checkout at the desired source revision |
| `FORGE_CI_LIVE_CONSUMER_VISIBILITY` | `public` (required) |
| `FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE` | Trusted Maven/JDK **25** job image, full registry reference pinned with `@sha256:…` |
| `FORGE_CI_LIVE_SERVICE_BRANCH` | Logical scratch source branch, e.g. `feature/service-trial`, different from `main` |
| `FORGE_CI_LIVE_SERVICE_READINESS_URL` | Project-owned readiness endpoint, e.g. `http://127.0.0.1:8080/actuator/health/readiness` |
| `FORGE_CI_LIVE_SERVICE_FIXTURE` | Optional absolute regular CI script path; never production credentials |

Filter: `^TestForgejoLayer_ConsumerDevelopment$` in
`./internal/livetest/conformance` with `-tags=live`. The operator's normal live
suite driver supplies the contract and session; no controller-side Java,
Maven, application container run or separate API script is needed. The container
runner must permit the existing Buildah setup helper and apt package installation.
The readiness job uses that same helper to provision Podman, jq and nftables in
its isolated job storage, rather than assuming they are in the runner's default
image. Netavark needs `nft` for the loopback port mapping.
Podman must trust and anonymously pull the public lab
registry; configure that in the runner fixture rather than bypassing TLS.

Use Go **1.27.3** with its matching `GOROOT` and exact pinned toolchain directory
first on `PATH` for local checks:

```sh
env GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test ./internal/livetest \
  -run '^TestMavenServiceDevelopmentPreservesSource$' -count=1
```

The source checkout is admitted once at HEAD; the chosen logical branch in the
scratch repository receives that source plus CI overlay and the standard trial
canary. Policy `main` and the selected branch have different commits. Dispatch
runs on policy `main`; `snapshot-images.yml` resolves the selected branch once,
then its exact SHA feeds both image construction and the checked Maven job.
The source POM, business code, Containerfile, Maven checks and rules are unchanged.

The generated CI declaration is container-only: the original Containerfile
builds the image itself, and no unused artifact-first producer is scheduled.
The separate Maven `app` job uses the shared builder, retaining its normal
checks and tests, then copies actual `target/<artifactId>-*.jar` outputs. This
is intentionally a second checked build: **the checked JAR is not asserted to
be the JAR inside the image**. Artifact-first projects should use the existing
shared snapshot producer/context recipe instead of this container-first case.

The runtime fixture script runs inside the Java job and writes
`trial-runtime/runtime.env` and optional files. These are transferred in the
same run, mounted read-only at `/trial`, and passed to the service with
`--env-file`. Only disposable, nonsecret trial material belongs there. The
runtime is pulled and started by immutable digest. Readiness makes at most 60
HTTP requests, each bounded to two seconds, with two-second retry intervals;
container cleanup uses the exact created container ID. The input URL names the
container port and endpoint; Podman maps it to a random loopback host port, so
the probe cannot accidentally accept another service already on the runner.
The runner fixture must support rootless Podman port forwarding.

The acceptance binds the frozen source branch/SHA, registry digest and OCI
revision/branch labels, actual checked JARs, and HTTP readiness evidence. It
rejects unexpected registry aliases, Git tags/releases and movement of either
scratch branch. Existing SDK/session ownership and package cleanup remain in
charge. Successful job traces are retained by the existing log protocol.

## Wallet attestation issuer case

Set the fixture input to the absolute path of
`test/live/cases/wallet-attestation-issuer/service-fixture.sh` in this engine
checkout. It generates a one-day EC PKCS12 key with Java's `keytool` in the
Java 25 job and supplies inert issuer metadata using `example.invalid` URLs.
The current source POM declares no external database or Testcontainers
dependency. If a future project adds such dependencies, provision its runner
fixture, tool path and build environment; do not disable its checks.

The inspected original wallet Containerfile copies only `.mvn`, `mvnw`,
`pom.xml` and `src` into its builder. Its POM references configuration below
`development/`, which that image build does not copy. It also explicitly skips
tests/checkstyle/formatter in its own package command and builds a layered
runtime JAR independently. A failure caused by those original inputs is a
**CI integration blocker**, not an application/linter defect to fix in this
trial. Preserve the source Containerfile and report the exact failing job.

No live image/service result is implied by offline fixture tests. The existing
stable signed-file result is separate evidence; a future successful run must
provide the actual digest and readiness artifacts before development is marked
accepted.
