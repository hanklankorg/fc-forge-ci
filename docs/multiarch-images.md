# Checked multi-architecture images

The consumer owns its platform selection, native runner routing, product smoke
tests, release names and base-image policy. Forge CI owns immutable image
validation, registry-only index assembly and checksum-bound SBOM composition.
The commands are provider-neutral and contain no consumer-specific policy.

## Builder identity and cache inputs

The selected builder is behind the same interface for both Buildah and Kaniko.
Consumers that bind image-producing tools into a base-input ID can capture its
identity without naming backend executables or probing container storage:

```bash
forge-ci container builder-identity
```

The command prints one opaque SHA-256 material ID. A consumer includes this value
in its deterministic cache inputs without knowing backend names, executable paths
or an identity-record schema. Operator configuration selects the backend through
`CI_IMAGE_BUILDER`; changing either the backend or its executable changes the ID.
Capture it on each native build runner, because executable bytes can differ
between architectures. No builder is run, installed, or probed, and no registry
is contacted. `--format=json` additionally emits the ID and backend details for
provenance or diagnostics; consumers need not interpret those details. The ID
identifies bytes and backend selection; it is not a signature verification.

Keep the approved job-image digest and engine pin as explicit provenance inputs
too. This command does not replace job-image admission, source identity,
dependency selection or the consumer's base-graph policy.

For bounded whole-architecture reuse, `build-arch --stage --cache-tag <slot>`
accepts repeated `--require-label` checks. A hit is copied by immutable digest
to the ordinary staged layout; no build or registry write runs in that job.
Use a content-derived revision for reusable bases. `assemble --from-layouts`
can generate per-platform SBOMs with `--sbom-output-template` beside its
`--scan-output-template`, and refresh slots using `--cache-tag-prefix` only
after every child passes its gate and the index is assembled. This is not
registry-backed intermediate-layer caching, and cache slots are not signed bases.

## Build and publish the index

`container multiarch-image build-arch` and `assemble` accept `--tag` for an
explicit index tag name. Architecture tags append `-<arch>`. Without `--tag`,
the existing `--tag-prefix` plus source-commit naming remains unchanged.
`--tag` and `--tag-prefix` cannot be combined.

Each producing job builds and checks its own architecture. Run `assemble` only
after **every selected build, scan and smoke job succeeds**. Metadata is not
proof that a job's tests passed. Registry-backed candidates can exist before
the index; `--stage` and `--from-layouts` retain the stronger whole-matrix upload
barrier when the caller transfers local layouts instead.

For example, after both native jobs have produced `widget` metadata:

```bash
forge-ci container multiarch-image assemble \
  --server-url https://forge.example \
  --repository acme/widget \
  --source-sha "$SOURCE_SHA" \
  --image-repository registry.example/acme/widget \
  --tag staging-v1.2.3 \
  --name widget \
  --archs $'amd64\narm64' \
  --require-label "org.opencontainers.image.revision=$SOURCE_SHA" \
  --auth-file /workspace/registry-auth.json \
  --manifest-output /workspace/dist/index.json
```

The records are `widget-arch-amd64/widget-amd64.json` and the equivalent ARM64
path, relative to the current workspace. Their schema is:

```json
{
  "arch": "amd64",
  "tag": "registry.example/acme/widget:staging-v1.2.3-amd64",
  "digest": "sha256:<64 lowercase hex>",
  "ref": "registry.example/acme/widget@sha256:<same 64 lowercase hex>"
}
```

Custom builders can provide these records after their own checks. The engine
requires agreement between their architecture, expected tag, digest and
repository. It inspects the actual immutable child platforms and all repeated
`--require-label key=value` constraints before writing an index. Staged mode
also checks the local image labels before uploading any layout. An empty label
value is supported but the label must exist.

Supply the revision constraint explicitly when source identity is required.
`--source-sha` controls build labels and default tag naming; the commit hash and
metadata alone are not an authenticated release or a replacement for label checks.

The registry adapter writes only the index: it reuses the children already in
the repository. The served index digest must equal the locally constructed
index's digest. `--manifest-output` retains the exact checked bytes, without
JSON re-encoding or an added newline, for the offline SBOM step.
The snapshot path must not alias authentication inputs, architecture metadata,
or the generated image metadata file. Parent-directory aliases are resolved even
when the destination file does not yet exist. Directory outputs, final-component
symlinks and unresolved links are refused.

## Verify a reused index

`container multiarch-image verify` takes the same metadata, tag, architecture,
repository and label inputs, plus `--ref` with the tag-free index digest
reference. It performs no registry writes. A mutable tag is refused.

It checks the actual child platforms, the index's byte digest and the exact
platform/child set. Missing, extra, repeated and substituted children fail.
A single-image manifest is accepted only when its bytes are the sole expected
child. `--manifest-output` is optional; without it no metadata file is written.
The operation emits `image-ref` and `image-digest` CI outputs after validation.
Assembly and verification both read `IMAGE_ARCHS`; an explicit `--archs` value
takes precedence. Push-retry flags belong to the publishing commands only.

The current native build/assembly interface supports Linux AMD64 and ARM64.
Index verification does not execute either architecture's code.

## Compose the index SBOM

For staged builds, `container multiarch-image assemble --from-layouts` can
perform this composition directly. Alongside `--scan-output-template` and
`--sbom-output-template`, pass `--manifest-output`, `--index-sbom-output` and
`--sbom-url-prefix`. The prefix is the public HTTPS directory where the caller
will publish the unchanged platform inventories. The operation scans the staged
bytes before any upload and uses their recorded child digests and the checked
served index to compose the index BOM without re-pulling images. It emits
`image-sbom-sha256` for the ledger pin. Evidence destinations must be distinct
and must not overwrite authentication or staged image inputs. The shared
`release-images.yml` uses this path for scanned release images.

For evidence produced separately, the existing offline command below remains
available.

Keep each native CycloneDX document unchanged. Supply a version-1 manifest such
as `platform-sboms.json` alongside the verified `index.json` and native BOMs in
the release evidence directory:

```json
{
  "version": 1,
  "platforms": [
    {
      "platform": "linux/amd64",
      "image_ref": "registry.example/acme/widget@sha256:<amd64 digest>",
      "file": "sbom-amd64.json",
      "url": "https://downloads.example/widget/v1.2.3/sbom-amd64.json",
      "sha256": "<producer checksum of sbom-amd64.json>"
    },
    {
      "platform": "linux/arm64",
      "image_ref": "registry.example/acme/widget@sha256:<arm64 digest>",
      "file": "sbom-arm64.json",
      "url": "https://downloads.example/widget/v1.2.3/sbom-arm64.json",
      "sha256": "<producer checksum of sbom-arm64.json>"
    }
  ]
}
```

```bash
forge-ci sbom index \
  --image-ref "$INDEX_REF" \
  --root /workspace/dist \
  --index-file index.json \
  --platform-sboms platform-sboms.json \
  --output /workspace/dist/image-sbom-widget.cyclonedx.json
```

This is an offline operation. All inputs are regular files under `--root`;
traversal, symlink files, unknown manifest fields and trailing JSON are refused.
The index snapshot must hash to `--image-ref`, and its declared children must
match the complete platform record set. Each native BOM must match its
producer-supplied checksum. Overwriting an input document is refused.

The output is deterministic CycloneDX 1.6 with one container component per
platform and checksum-bound external BOM references. Native package graphs and
`bom-ref` values are not merged or rewritten. The command checks CycloneDX
headers and byte bindings, not full schema validity or inventory accuracy.
Use checked assembly or verification to establish actual child config identity
before composing from an index snapshot.

Public URLs must be absolute HTTPS without user information, query strings or
fragments. URLs are not fetched, and availability is not established here.
The caller must publish every referenced native document at the declared URL.
Register those files and the index BOM in the release-file manifest before
computing the final signing hand-off digest.

The command prints the index BOM checksum and emits `sbom-path`, `sbom-sha256`
and `evidence-files` CI outputs. Pass the output path and checksum to
`container ledger add --sbom ... --sbom-sha256 ...`. The existing signer then
attests those exact bytes instead of regenerating a host-selected inventory.

Both `--manifest-output` and `sbom index --output` require file destinations.
The stdout sentinel `-` is rejected before any output is written. Stdout remains
the machine-readable reference or checksum channel, separate from artifact bytes.

## Verification scope

Regression cases cover complete and incomplete architecture sets, duplicate
requests, wrong OS/digests, label mismatches, conflicting tags, read-back drift,
single-manifest substitution, deterministic SBOM links, native document drift,
unsafe paths, malformed manifests and credential-bearing URLs. Staged failures
are checked before registry writes using an in-process registry fixture.

These checks do not establish runner isolation, native smoke success, hosted
credential sharing, public URL availability or a production release's signing
authority. Those remain caller and hosted-acceptance responsibilities.

### Staged evidence scenarios

The provider-free suite exercises the actual application and workflow shell:

| Scenario | Required result |
| --- | --- |
| One selected platform or AMD64 + ARM64 metadata | Every scanned layout has an unchanged inventory bound to its child digest in the checked index BOM. No second image pull or scan. |
| Distroless, Alpine and arbitrary flavor names | Evidence paths follow the ledger's flavor/role policy, independently of the build name. |
| Multiple images in one workflow | Exact named artifacts retain each image's platform inventories, index snapshot and index BOM without changing their bytes. |
| One platform fails the scan/gate or produces no SBOM | No image upload and no index BOM; existing complete-set publication tests remain mandatory. |
| Missing inventory/index/URL inputs; unsafe public URL | Refuse before scanners or registry writes. |
| Colliding paths, symlink aliases, auth/metadata/layout overwrite | Refuse before scanners or registry writes. |
| Different inventory paths with the same public download filename | Refuse before scanners or registry writes; platform inventory filenames must be distinct. |
| Scanned release ledger has no index-BOM checksum | Refuse to record the release image. |
| Legacy consumer `enable-scan: false` | Exported/published layouts still require vulnerability scanning; administrator report-only enforcement retains findings and does not remove SBOM requirements. |
| Base image | Retain all inventories and the existing first-platform premade signing contract. |

Existing index-SBOM and signer tests cover missing/duplicate platforms, wrong
child/index digests, changed inventory bytes and ledger checksum mismatches.
Dual-platform fixtures construct OCI metadata only; they do not execute foreign
architecture code. Live rollout acceptance remains a separate native profile:
verify original scans, published child/index digests, retained inventory hashes,
runtime evidence and exact-document signing through the guarded scratch workflow.

### Native mechanism acceptance

Run the guarded `TestForgejoLayer_ResolvedImagePlan` against the exact engine
and workflow revision selected for adoption. Exercise cold, one-sibling reuse
and fully warm cache phases with two small base images and a final-release
canary. Use actual build/cache logs as the cache oracle; digest equality alone
does not establish reuse.

Check retained scans/inventories, registry-served child/index digests, marker
bytes, index-BOM platform/digest/URL/hash bindings and the ledger's exact BOM
checksum. Retain the run evidence and exact-resource cleanup receipt. Acceptance
is specific to the native platform and mechanism tested; it does not establish
other architecture execution or complete consumer release acceptance.
Consumer adoption requires a published, reviewed engine/workflow tuple.
