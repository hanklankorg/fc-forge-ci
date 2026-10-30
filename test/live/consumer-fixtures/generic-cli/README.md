<!-- SPDX-FileCopyrightText: The Forge CI Authors -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Generic CLI GitLab stable adapter — preparation checkpoint

## Native Forgejo stable transport

Append `forgejo` as the optional fifth prepare argument (default: `gitlab`).
This stages `.forgejo/workflows/release.yml`, replacing other workflow entries
only in the private index so the SDK sees one coherent engine pin. Existing
`.gitlab/` scripts/metadata remain the single shared check/producer/identity
implementation; the Forgejo workflow invokes them directly. No source format
conversion, release matrix or container-snapshot flow is used.

The native graph authorizes the signed request on `CI_RUNNER_LABEL` with a
verified engine before prepare. Source checks/build run in a fresh job on
`CI_RUNNER_LABEL_CONTAINER` when supplied by the existing SDK contract, using
`CI_JOB_IMAGE`; the Go lane invokes existing setup-buildah. Preparation and
signing use the existing reusable workflows and restricted runner label; no
source job receives signing secrets. The shared release-Git helper detects the
same `.gitlab/` metadata and runs the existing downloaded executable/identity
oracle on Forgejo as well.

Operator: prepare a new clean checkout with the normal four arguments plus
`forgejo`, then use `FORGE_CI_LIVE_CONSUMER_VISIBILITY=public just
test-live-forgejo-consumer /absolute/prepared-checkout release version=0.8.6
CI_AUTH_IMAGE=<reviewed-digest-pinned-restricted-job-image>
CI_JOB_IMAGE=<reviewed-digest-pinned-native-Rust-job-image>`.
Exact filter: `^TestForgejoLayer_ConsumerTrial$`. Cargo bump is configured by
the overlay; Go uses no version bump and can request `version=0.0.1`.
The SDK supplies current layer/binary/signer bindings and ephemeral request
trust; placeholder pins in the recipe are replaced, never operator digests.

Remaining prerequisites are operator-owned: Forgejo support for Nano's SHA-256
repository format, reviewed anonymously readable native job/auth/signer images,
and ordinary/capable runner contract labels. No live support is claimed by
compile/unit tests. Prefer Nano first; if Forgejo refuses SHA-256, report that
capability gap and use a clean current Forge CI SHA-1 source, never convert Nano.
The Go lane still requires verification of its real Buildah integration
capability/evidence before acceptance is declared.

### Original-source checks versus prepared-release checks

The native Forgejo graph is `authorize -> checks -> prepare -> build ->
sign-and-publish`. The capable checks job checks out the immutable event/request
commit and independently revalidates its HEAD, annotated request and SSH
signature before executing source commands. Full original application tests
(including repository guards and shell contracts) run here, before version
preparation, with full informational lint and no exclusions. Prepare cannot
start if those tests fail.

The separate prepared-source job retains fresh request signature verification,
both exact annotated tag fetches, and `validate-tag` parent/change-scope checks.
It runs the shared producer on the exact prepared SHA. Cargo metadata must
match the requested version; workspace bin tests and nanolinter-ci tests still
run post-bump and block, as do native compilation and every downstream release
verification. The complete original repository guard suite is not repeated on
this deliberately version-prepared tree.

Run 281 correctly exposed that generic Cargo preparation changes Cargo project
versions, not Nano's Debian packaging changelog. The original Debian/Cargo
consistency guard belongs to the admitted source gate. This file-only fixture
does not build or publish Debian packages and makes **no claim** that the
prepared tree is ready for Nano's distribution-packaging release. Its Cargo
version is 0.8.6 while unchanged Debian metadata remains 0.8.5; any future Debian
release needs an owning project/version-preparation solution, not a waived
guard or a fabricated packaging edit here. No CLI production input/binary
binding changes are required by this sequencing correction; prepare a fresh
fixture/engine snapshot for a subsequent operator run.

## Acceptance policy: full Nanolinter diagnostics, blocking application gates

Both Go and Nano adapters run the full original Nanolinter plan without check
exclusions or warning-policy overrides. Its completed outcome (exit 0/1) is
informational; full output, summary and an explicit numeric exit status remain
in the job log. Prior findings, including the 64 KICS findings, remain unresolved.
Missing tools, setup/build failures, invocation/configuration errors and
cancellation remain blocking. Original application tests, release builds,
signing/publication and downloaded-payload verification remain blocking.

Prepare a **new snapshot** with four arguments and no exclusion flag:

```sh
bash /home/jos/development/github.com/diggsweden/r3/forge-ci/test/live/consumer-fixtures/generic-cli/prepare.sh \
  /home/jos/development/codefloe/itiquette/nanolinter \
  /tmp/opencode/digg-nano-gitlab-stable-diagnostic-lint-fixture \
  cargo cfc3aefb659d87c314dad2c33e5ad117bd1d52b6d4633d107adc4fecca59619e
```

The destination must not exist. Source identity records `lintPolicy:
"informational"`, `excludedChecks: []` and the explicit coverage limitation;
this document is also exported and checked by the existing signed identity
oracle. Old KICS-exclusion flags are rejected, and excluded-check metadata is
refused by the check wrapper. Active runs on older snapshots cannot change in
place: they retain their old policy and require a new preparation/run.

The adapter separates each source's original `just verify` groups rather than
ignoring its exit status. Nano builds its source binary and runs original
`check-tools`; Go retains `_lint-linux-platform` and blocking `go mod tidy -diff`
and independently requires `nanolinter check-tools`. Both retain the justfile
format check, directly invoke the same full `nanolinter verify`, record its
status, and then run original `just test`. Direct invocation preserves
Nanolinter's exit vocabulary instead of `just` flattening failures into exit 1.
No source justfile, lint configuration, pin or application file is patched.
**A successful acceptance run is not a full original `just verify` pass.**

The Go adapter records `.gitlab/generic-cli-checks` as `buildah-integration`.
Release acceptance reads that explicit marker and the `go` kind from regular
Git blobs at the admitted commit, and requires the contract's
`runner.privileged_label` for `Check: consumer checks` only. Restricted
`Check: release request` authorizes the signed immutable request first; the
consumer-check job freshly checks out and revalidates it before the original
suite runs. Cargo and unmarked consumers keep untagged checks. This grants no
stronger runner to an arbitrary merge-request gate. Prepare a new Go snapshot
to carry the marker; older snapshots do not acquire it implicitly.

Run the existing public GitLab consumer trial with this new checkout,
the reviewed v3 job/build archives and reviewed signer archive, and settings
`version=0.8.6 version-bump=cargo`; filter `^TestGitLabRelease_ConsumerTrial$`.

Uses the existing `TestGitLabRelease_ConsumerTrial` and `templates/release*.yml`
policy. This fixture has unit verification, but has not run against GitLab.
No release acceptance is claimed.

## Operator preparation

Run `bash prepare.sh ABSOLUTE_SOURCE ABSOLUTE_NEW_CHECKOUT go|cargo FULL_SOURCE_COMMIT`
using this directory's absolute script path. Supply the full reviewed commit,
not the abbreviated `9d10` or `cfc3` identifier. The source must be clean.
Preparation writes only a disposable clone's objects, index and checkout;
the clone borrows source objects and must be retained alongside its source.
Only `.gitlab/` and `.chglog/` entries are overlaid. Application source and
Cargo versions are not edited. The later SDK Cargo preparation job owns its bump.
This preparer makes an unsigned scratch snapshot, never a release request tag.
The destination must be new, have an existing parent, and resolve outside the
source checkout. The preparer preserves the Git object format and source entry
modes. CI directory symlinks are refused before cloning. Forced materialization
is confined to the newly created private clone; a clean checkout is required
before success is reported. Do not reuse a partially prepared destination.

The private overlay includes four regular REUSE sidecars:
`.gitlab/generic-cli-kind.license`, `.gitlab/generic-cli-source.json.license`,
`.chglog/CHANGELOG-keepachangelog.tpl.license`, and
`.chglog/CHANGELOG-minimal.tpl.license`. Metadata stays plain text/JSON;
sidecars supply attribution without changing application policy. Shell/YAML
files retain their inline SPDX headers. Templates retain their original Digg
attribution, independent of the engine-only demo annotation. Sidecars belong
to the CI overlay, not the original source commit or release identity payload.

Focused fixture tests run shfmt and REUSE against materialized synthetic
SHA-1/SHA-256 overlays when those tools are available. Set
`FORGE_CI_TEST_shfmt` and `FORGE_CI_TEST_reuse` to absolute tool paths for
explicit preflight; omitted/unavailable tools produce a visible test skip.

From the engine root, the existing SDK command is:

```sh
FORGE_CI_LIVE_CONSUMER_VISIBILITY=public \
just test-live-gitlab-consumer-release /absolute/prepared-checkout \
  /absolute/reviewed-job.tar /absolute/reviewed-build.tar /absolute/reviewed-signer.tar \
  version=0.0.1
```

For Nano use `version=0.8.6 version-bump=cargo` instead. These are example runtime
settings, not constants in the adapter. The exact existing filter is
`^TestGitLabRelease_ConsumerTrial$`, narrowed to `/public` by the visibility setting.
Use disposable mode (no demo name), the existing guarded GitLab full-release
profile and a Linux amd64 runner. Native Cargo target is
`x86_64-unknown-linux-gnu`, not a multi-target matrix. Keep the normal SDK
ephemeral signed-request and isolated signer lifecycle.

## Archive reuse and capability gaps

`rust-job.Containerfile` is the maintained request-check/build role layered on
an operator-reviewed immutable Rust tool image (`SOURCE_IMAGE`, supplied by the
operator, not a floating default). It adds SSH request verification and gettext
development prerequisites, retains UID 65532, and checks Rust 1.99.1 plus the
native GNU amd64 target. This does not enlarge the product runtime flavor. Build
and export it with the existing local Podman/Buildah container route, then
record the resulting archive checksum separately from its image/config digest.
The job needs a writable per-job HOME/cache for tools such as opengrep; an
entirely read-only container without that state is not a representative job.

The standalone signer comes from `containers/signer/Containerfile`; its build
and runtime verifier require `ssh-keygen` and reject compiler toolchains. Keep
the signer context confined by its maintained Containerfile ignore policy.

* Reuse the reviewed job archive only if it supplies Bash, Git, jq, `just`,
  the selected toolchain and **all** tools needed by the source's `just verify`.
  The request checks run in this image, not in the build image.
* Go requires Go **1.27.3**, including for the public codefloe module dependency.
  Builder needs this compiler and the source dependency closure/access.
* Nano requires Rust **1.99.1**, native GNU amd64 Rust target, linker/C toolchain,
  pkg-config/gettext development libraries and locked Cargo dependencies.
  The Cargo prepare-version job also uses the build image.
* Existing reviewed signer archive is reusable if it satisfies the SDK's
  standalone Cosign/SSH/GPG signer checks. The current consumer trial requests
  distro-signing credentials even for this file-only release; retain its normal
  ephemeral credential provisioning. Do not put source or compilers in the signer.
* Archive paths are operator inputs. The existing SDK uploads them and supplies
  digest-pinned image references to the policy. No new images are built here.

The full Nanolinter outcome is informational under this acceptance policy;
application tests and release gates remain blocking. Provisional Nano findings
must be retained and reported; this does not resolve or reclassify them as passes.
Retain the normal SDK job logs and release inventory. The unsigned handoff has
the actual executable, original source commit/object format, prepared release
commit/version, binary SHA-256, notes, checksums and release-files declaration.

Shared release-Git acceptance now detects the adapter's regular Git blobs at
the admitted request commit. It verifies the downloaded checksum bundle with
the existing `VerifyBlob` contract, parses subjects with the shared provenance
parser, rejects duplicate subjects, and hashes the downloaded executable and
both identity documents. Source identity must equal the admitted blob; build
identity must name the actual signed prepared-release commit and requested
version. The executable is inspected as ELF64 little-endian amd64, without
running downloaded code. This hook applies to every forge using shared
release-Git acceptance; no parallel signing implementation was introduced.

Remaining operator prerequisites: independently establish archive capabilities,
provide the full reviewed clean source commit and disposable checkout, and run
the guarded live acceptance above. GitLab must support the source's object
format (Nano's SHA-256); no SHA-1 conversion is performed. Status remains
**prepared, live acceptance unproven** until that run passes.
