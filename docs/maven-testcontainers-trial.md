# Original Maven application tests with Testcontainers

The original Wallet Account tests start PostgreSQL through Testcontainers. A
plain Maven job has no Docker API and cannot satisfy that contract. Do not skip
those tests or present a missing engine as an application pass.

The existing public Forgejo Maven application trial can opt into a job-local
test-engine sidecar with `FORGE_CI_LIVE_MAVEN_TESTCONTAINERS_IMAGE`. The image must
be an operator-reviewed immutable digest. The controller requires the public,
disposable, native amd64 Maven application fixture and the acknowledged
privileged runner label from the v5 contract before publishing its workflow.

Only the key-free build job gains the service and Docker client environment.
Preparation, signing and publication retain their existing jobs and credentials.
No host engine socket or host port is mounted/published. The service's unencrypted
Docker API is reachable on the runner-selected network. The accepted Compose
runner uses the shared lab network, so this is not per-job network isolation;
use only trusted disposable workflows and serialize these trials. The job and
service are removed by normal runner cleanup. Use the full-release runner
acknowledgement and its existing guarded reconciliation/retirement procedures.

Example operator inputs, alongside the normal v5 contract and reviewed signer
image:

```bash
export FORGE_CI_LIVE_CONSUMER=/home/operator/consumers/wallet-account
export FORGE_CI_LIVE_CONSUMER_VISIBILITY=public
export FORGE_CI_LIVE_CONSUMER_EVENT=release
export FORGE_CI_LIVE_CONSUMER_SETTINGS=version=4.0.1
export FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE='docker.io/library/maven:3.9-eclipse-temurin-25@sha256:96f175708f766ea049ab6c794a85335d238894c75f8eb364bddd98086a5b7cfd'
export FORGE_CI_LIVE_MAVEN_TESTCONTAINERS_IMAGE='docker.io/library/docker@sha256:2a232a42256f70d78e3cc5d2b5d6b3276710a0de0596c145f627ecfae90282ac'
just test-live '^TestForgejoLayer_ConsumerTrial$/public$'
```

That service digest resolves the reviewed Docker 28 DinD image. It supports the
Docker API used by the original Testcontainers Java 1.21.4 dependency; Docker 29
raises its minimum API and is not an interchangeable fixture choice. The source
POM and tests remain unchanged. All original tests run, and the ordinary signed
release acceptance still verifies the prepared Git state and release bundles.

The fixture does not claim GitLab/GitHub Testcontainers parity or service runtime
readiness of the released application. It supplies infrastructure for the
original Maven tests in this one Forgejo lane. A unit fixture proves that the
source blobs remain admitted and unchanged and that no credential-bearing job
receives the service configuration; native acceptance is a separate receipt.

## Public Compose acceptance receipt — 2026-10-31

The original Wallet Account source passed Forgejo run **332**, release
**v4.0.1**, using the contract-bound runner fixture (`859790be`) and the normal
default-enforcing engine. The retained Maven trace reports **124 tests, zero
failures, zero errors and zero skips**, including the original PostgreSQL-backed
component/repository tests. Seven release files and three Cosign bundles were
verified by the existing host oracle. Original source/POM/tests were unchanged.
This is signed-JAR and original-test acceptance, not a deployed-service proof.

The original Gateway trial **334** also reached its Valkey-backed tests:
**438 tests, zero failures/errors, 17 skips reported by the original suite**.
Its release handoff then exposed a fixture assumption: the original POM sets
`build.finalName` to an unversioned JAR name. The application overlay now requires
exactly one `target/*.jar` and copies its unchanged bytes to the versioned release
asset name. Missing or ambiguous JAR output fails the fixture. No POM or test is
rewritten. Gateway retry **336** then hit original authentication-test HTTP
timeouts and the existing job deadline; its signed-release verdict remains
unresolved.

The separate original Gateway CI Test repository passed corrected trial **337**
with **412 tests, zero failures/errors and 16 native suite skips**, followed by
release **v4.0.1**, seven release files and three independently verified Cosign
bundles. This live-verifies the custom-JAR handoff at fixture snapshot `15502517`
under the default enforcing engine. It does not close Gateway's retry failure or
the declared runtime-image and ecosystem-service lanes.
