# Checked multi-architecture images

These commands check images built on native runners per architecture, write
the multi-architecture index to the registry and compose its SBOM. They work
on every forge. Your project chooses its platforms, runners, smoke tests,
release names and base images.

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
provenance or diagnostics. The ID identifies bytes and backend selection; it
is not a signature check. Keep the job-image digest and engine pin as cache
inputs too.

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
the name is `--tag-prefix` plus the source commit. `--tag` and `--tag-prefix`
cannot be combined.

Each producing job builds and checks its own architecture. Run `assemble` only
after **every selected build, scan and smoke job succeeds**. Metadata is not
proof that a job's tests passed. Registry-backed candidates can exist before
the index; `--stage` and `--from-layouts` retain the stronger whole-matrix upload
barrier when the caller transfers local layouts instead.

For example, after both native jobs have produced `widget` metadata:

```bash
forge-ci container multiarch-image assemble \
  --server-url https://forge.example \
  --repository example-org/widget \
  --source-sha "$SOURCE_SHA" \
  --image-repository registry.example/example-org/widget \
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
  "tag": "registry.example/example-org/widget:staging-v1.2.3-amd64",
  "digest": "sha256:<64 lowercase hex>",
  "ref": "registry.example/example-org/widget@sha256:<same 64 lowercase hex>"
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
JSON re-encoding or an added newline, for the offline SBOM step. An output
path that aliases an input, a directory or a symlink is refused.

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
perform this composition directly. `--from-layouts` always scans: it requires
`--scan-output-template`, and the administrator policy decides whether findings
block. Alongside `--scan-output-template` and
`--sbom-output-template`, pass `--manifest-output`, `--index-sbom-output` and
`--sbom-url-prefix`. The prefix is the public HTTPS directory where the caller
will publish the unchanged platform inventories. The operation scans the staged
bytes before any upload and uses their recorded child digests and the checked
served index to compose the index BOM without re-pulling images. It emits
`image-sbom-sha256` for the ledger pin. Evidence destinations must be distinct
and must not overwrite authentication or staged image inputs. The shared
`release-images.yml` uses this path for every release image.

For evidence produced separately, use the offline command below.

Keep each native CycloneDX document unchanged. Supply a version-1 manifest such
as `platform-sboms.json` alongside the verified `index.json` and native BOMs in
the release evidence directory:

```json
{
  "version": 1,
  "platforms": [
    {
      "platform": "linux/amd64",
      "image_ref": "registry.example/example-org/widget@sha256:<amd64 digest>",
      "file": "sbom-amd64.json",
      "url": "https://downloads.example/widget/v1.2.3/sbom-amd64.json",
      "sha256": "<producer checksum of sbom-amd64.json>"
    },
    {
      "platform": "linux/arm64",
      "image_ref": "registry.example/example-org/widget@sha256:<arm64 digest>",
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

## What the checks do not establish

These checks do not establish runner isolation, native smoke success, shared
credentials on hosted runners, public URL availability or a release's signing
authority. Those stay with the caller. Checks of two-platform metadata do not
run the other architecture's code.
