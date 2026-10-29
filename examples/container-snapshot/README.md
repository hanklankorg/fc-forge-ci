# Container snapshots

A container-only example: the image contains `hello.txt` and has no executable.
No ecosystem build or package publication is needed. Declaring the container
enables snapshot publication by default, with inherited scan severity and image
SBOM policy; the example does not disable security checks.

## GitHub

Copy `artifacts.yml` to `.forge-ci/artifacts.yml`, `Containerfile` and `hello.txt`
to the repository root, and `github/release-snapshot-workflow.yml` to
`.github/workflows/`. Allow the GitHub token to publish packages to GHCR.
The default image repository is `ghcr.io/<owner>/<repository>`.

Run **Container snapshot** manually and enter any developer branch in
`source-branch`, for example `feature/login`. An empty input uses the workflow's
selected branch. The root orchestrator freezes the chosen branch once and passes
that SHA to artifact and image builds. The collected `container-images-json`
output contains source identity, platforms and digest references.

The caller uses the same `v0.90.0` baseline as the other examples; the initializer
rewrites workflow/binary pins to its selected version. For this in-progress
feature, select a reviewed revision containing the snapshot adapters and matching
binary/runtime images as described in [runtime images](../../docs/runtime-images.md).
The baseline tag alone is not evidence that unreleased functionality is available.

Set `publish-containers: false` under `with:` to opt out. For another registry,
set `container-image-repository` (for example
`registry.example.com/example-org/hello`), `container-registry-user`, and secret
`container-registry-password`. The standalone reusable `snapshot-images.yml`
instead takes `branch`, optional `source-sha`, `image-repository`, `registry-user`
and secret `registry-password`.

## Forgejo and GitLab

The same declaration and context work with the native
[Forgejo snapshot workflow](../../docs/forgejo.md#snapshots) and
[GitLab snapshot component](../../docs/gitlab.md#snapshots). Pin the layer SHA,
admitted job images and native runner coordinates according to those guides.
This example needs no artifact producers. Adding supported Go, Cargo, Maven,
Gradle JVM or npm artifact-first dependencies enables automatic native producers
by default. A prepared context is an optional advanced same-run override.
Native `artifact-job-image-go`, `artifact-job-image-java`,
`artifact-job-image-node` and `artifact-job-image-rust` inputs must be digest-pinned
when supplied. Forgejo also reads `SNAPSHOT_ARTIFACT_JOB_IMAGE_GO`/`JAVA`/`NODE`/`RUST`
variables, falling back to existing host tools. GitLab falls back to its trusted
`job-image`, which must supply the corresponding toolchain.

## Adding artifact-first inputs

The shared plan freezes `artifacts[]`, source SHA and `source_date_epoch`.
`build-artifact` uses existing ecosystem builders and their typed settings.
The GitHub root orchestrator sets `use-build-artifacts: true` to reuse the exact
artifact-stage uploads through `prepare-inputs`; standalone `snapshot-images.yml`
defaults to `false` and automatically builds the required producers.

`gather-context` verifies the producer set and bundles the inputs.
`restore-context` validates and restores only planned dependency paths in Go.
Producer receipts and inventories record file sizes, digests and canonical string
modes `0644`/`0755` so ZIP transport does not lose executable intent. Expected raw
Go/Cargo binaries are normalized as executables; other files are not blanket
chmodded.

Npm requires exactly one valid packaged tarball. For an artifact named `web`, its
isolated destination is `<context>/.forge-ci/snapshot-inputs/web`; the Containerfile
can copy the package relative to its context:

```dockerfile
COPY .forge-ci/snapshot-inputs/web/*.tgz /tmp/
```

This line illustrates the input path for a consuming application; the minimal
container-only example above has no npm package. See
[automatic artifact-first inputs](../../docs/workflows.md#automatic-artifact-first-inputs).

## Image contract

Final tags have the form
`1.2.3-feature-login-<branchhash>-a1b2c3d-snapshot`: the original branch hash is
always present and the final postfix is lower-case. Package snapshot versions
keep their existing convention. Builds run natively on amd64 and arm64 without
registry publication credentials. Publication waits for all builds and collection
requires exactly the planned image set.

Snapshots use signing `NONE` and do not move stable aliases or promotion stages.
Explicit snapshot OCI signing/provenance requests, build secrets and binary
extraction remain unsupported and fail explicitly. Minimal three-forge snapshot
acceptance is **pending operator execution**. See the
[shared snapshot contract](../../docs/workflows.md#snapshot-container-contract-all-three-forges).
