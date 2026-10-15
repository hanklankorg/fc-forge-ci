# Artifact and image flows

How a thing you declare becomes a thing you release. This page explains the
two flows that carry build output through the pipeline: **run artifacts**,
which move files between jobs, and the **image ledger**, which moves container
images from build to signature to final tag.

It builds up in three passes: an orientation with no jargon, then the
mechanics, then the details and the invariants. Read as far as you need.

## Orientation

Think of the repo as a factory.

`.forge-ci/artifacts.yml` is the **order form**. You write down what you
want built: a Java library, a Go binary, a container image. You do not say
how; you say what. Everything downstream is derived from that form.

The factory floor has separate **workstations**, which are CI jobs. They are
isolated, and they cannot hand things to each other directly. So to move a
finished part from one room to the next, you put it in a **labelled box on a
conveyor belt**, and someone downstream picks the box up by its label.

Two different things are called "artifact", and keeping them apart is the
single biggest aid to reading this codebase:

- The **thing you ordered**: a JAR, a binary. Declared in `artifacts.yml`.
- The **box on the conveyor** that carries it between jobs. It is temporary
  storage governed by the forge/workflow's retention and deletion policy, and
  can outlive the run. This is what `forge-ci artifact upload` and `download`
  deal with.

Container images are too big for the conveyor. They go straight into a
warehouse, which is a registry, as soon as they are built. So instead of
shipping the image around, the pipeline ships a **note about** the image.

That note is the **ledger**. It says: the image sitting at digest
`sha256:abc...` is the one that should eventually be called `v1.2.3`.

In workflows with a separate signing job, such as the Forgejo release flow,
the build job writes the note and the signer re-checks it against the registry.
The signer uses a release tag from trusted workflow inputs: a note asking for
`v9.9.9` when the authorized release is `v1.2.3` is refused. Job separation is
a property of the workflow, not of the ledger itself; the different signing
topologies are described [below](#how-each-flow-binds-build-to-signature).

Promotion never rebuilds. It moves labels. The image built at 10:00 is
bit-for-bit the image released at noon; only the tag pointing at it changed.

## How it works

### The artifact inflow

The chain is **declare, plan, build, upload, download, consume**.

1. A human writes `.forge-ci/artifacts.yml`. If it is absent and the repo
   has exactly one root ecosystem manifest (`pom.xml`, `go.mod`, and so on),
   one is derived in memory instead.
2. `config.Parse`, then `Validate`, then `Derive`. Parsing is strict
   (`KnownFields(true)` at every level), so a typo fails loudly rather than
   being silently dropped.
3. The result is a typed **`pipeline.ConfigPlan`**, emitted as JSON. This, not
   the YAML, is the contract every downstream job reads. It computes the
   **run-artifact names** once, centrally, so a build job and a publish job
   agree on what the box is called without having to coordinate.
4. Build jobs upload their output under those names. Downstream jobs, such as
   container build, SBOM, sign, release and publish, download them.

The transport depends on the forge:

| Forge | How run artifacts move |
|---|---|
| GitHub | Zips everything into one blob (v4 results service) |
| Forgejo | PUTs each file individually (Azure-pipelines-style container API) |
| GitLab | Runner-native `artifacts:` and `needs:` in job YAML, outside the CLI's upload/download port |

For `forge-ci artifact download`, the GitHub ZIP and Forgejo per-file
transports share the checks in `internal/domain/artifact/safe.go`: no absolute
entry paths, no `..`, no backslash separators, no symlinks, and a 5 GiB per-file
cap. GitLab runner-native transfers do not pass through that helper; the runner
owns their extraction behaviour.

### Two kinds of image, two lifecycles

`container` groups image commands under three names, and the split is a real
one rather than a filing choice:

| Group | Images | Addressed by | Lifecycle |
|---|---|---|---|
| `release-images` | what a release publishes | the release tag | staging → final + moving, with rollback |
| `base-images` | what those are built **FROM** | a content hash, `base-input-id` | staging → immutable final, plus freshness and retention |
| `ledger` | the record of the above | — | add, merge, validate |

They share verb names (`promote`, `cleanup`), but use different identities.
The consumer computes a base-input ID according to its own input-graph policy.
The engine accepts that ID as an opaque SHA-256-shaped value and checks its
agreement with signed evidence; it does not recompute the inputs. The input ID
is distinct from the OCI image digest. Correct cache selection depends on the
consumer including all relevant inputs, and a missing base is reported to the
caller for rebuilding. Upstream freshness is a separate check and policy.

Note that "base image" here is the consumer's own layer, not the
`forge-ci-runtime-*` images CI jobs execute inside. The engine builds those
for itself; it knows nothing about a consumer's, beyond the flags it is given.

### The image ledger

The lifecycle is **add, merge, sign, promote, cleanup**, with **rollback**
hanging off the side.

- **add**: the build job records one entry per image it pushed. `ledger add`
  either resolves the digest with `--capture-digest` or accepts an explicit
  `--ref`/`--digest` pair. Writes take an exclusive file lock, because parallel
  build jobs can write the same file, and re-adding an identical entry is a no-op.
- **merge**: for multi-container releases, each container's job writes its own
  small ledger and the promotion job concatenates and dedupes them into one.
- **sign**: the trust boundary. See below.
- **promote**: retags the recorded image and verifies the destinations. Never a
  rebuild. An immutable destination already serving the same digest is skipped;
  a different digest is refused. Moving pointers are copied to the recorded
  digest, including when they already serve it.
- **cleanup**: deletes the temporary `staging-` tag, but only after confirming
  the promoted tags really serve the digest.
- **rollback**: undoes a promotion by replaying a journal written before
  anything moved.

CLI release archives include `docs/schemas/release-images.schema.json` from the
same source revision as the binary. Consumers validating release-image ledgers
should use the bundled schema to keep it matched to the engine.

#### A release without images

`container release-images sign`, `promote` and `cleanup` take
`--allow-missing-ledger`. With it, a release that has no ledger is not an error:
when the default ledger `<dist-dir>/release-images.json` does not exist, the
command says so on stderr and exits 0 before it reads a registry credential or
logs in. Absence of that one file is all the flag admits. The dist directory
must exist, and a ledger named with `--ledger` (or its environment variable)
must exist. A ledger that exists is held to every rule with or without the
flag: a link, a non-regular file, a path outside the dist directory, content
that is not a JSON array (zero bytes and whitespace included) and a failed tag
or repository validation are refused, and so is a ledger without entries (`[]`
or `null`) where the verb needs entries, which `sign` and `promote` do.
`rollback` and `validate` do not take the flag.

#### Stage ladder and registry routing

The shipped ladder promotes one recorded digest through `:dev`, `:staging`, and
`:release`. Each rung resolves the source digest before copying and the
destination digest afterward; promotion never rebuilds or re-signs the image.

- With no `--stage-repo`, the destination stays in the source repository and
  the in-process OCI adapter performs the tag copy.
- `--stage-repo <prefix>` preserves the complete source path below the registry
  host, so `ghcr.io/org/api` becomes `<prefix>/org/api` and cannot collide with
  a sibling image.
- A destination in another repository or registry must use the configured
  `SignatureCopier` (`cosign copy`) so the image and registry-attached signature
  travel together. The command refuses a cross-repository copy when no signature
  copier is available.
- Release-stage cross-registry promotion also copies the immutable version tag;
  the moving `:release` pointer and immutable version must resolve to the same
  ledger digest.

The workflow-level gates and credentials for these rungs are documented in
[Workflow Architecture](workflows.md#image-promotion-ladder).

### The trust boundary

The same function, `entry.Validate(releaseTag)`, runs twice:

- At **record time**, inside `Append` (`internal/domain/imageledger/imageledger.go`).
- At **sign time**, inside the signer (`internal/app/container/ledgersign.go`).

That looks redundant and is not. The build job controls its own inputs, so its
own validation proves nothing to anyone else; it only stops honest mistakes
early. The signer re-runs the identical rule using **a release tag the signer
controls**, not one supplied by the build artifact. So a compromised or buggy
build job cannot get an image signed and promoted under a tag outside the
release it was authorised for. The rule is enforced by the party with
something to lose.

## The details

### Invariants

`Entry` is digest-first: validation requires a digest-pinned `Ref` that agrees
with `Digest`. Tag fields, when present, name explicit tags. Capture mode uses
`PinDigest` to build `Ref` from `DigestSource()` and a registry-resolved digest.
Explicit digest inputs are also accepted after structural validation. Recording
them does not prove the registry serves them; that is a separate verification
step.

Three axes on `Entry` are deliberately not collapsed into one:

| Field | Meaning |
|---|---|
| `Kind` | Free-form label such as `distroless`. Also the SBOM path fallback. |
| `ImageKind` | Selects the validation scope: release-scoped rules, or content-addressed base-image rules. Empty defaults via `OrDefault()` for legacy entries. |
| `Flavor` | Tag-composition input, orthogonal to both of the above. |

The scoping rule that does the security work: `final_tag` must equal the
release tag or `<releaseTag>-<suffix>`, and `candidate_tag` must be the exact
staging counterpart `staging-<finalName>`. That exact match ties one candidate
to one final tag, so a release promotes precisely the image staged for it.

### Layered checks

Four checks, independent on purpose:

1. **Record-time validation**: first line, not a boundary.
2. **Sign-time re-validation**: the boundary. Adds sign-only checks for
   repository confinement, required `kind` and `sbom`, ref/digest consistency,
   and base-lineage pairing.
3. **Registry re-resolution** (`Verify`, exposed as `ledger validate-digests`):
   catches a recorded digest the registry does not actually serve under any
   claimed ref.
4. **Repository confinement** (`ValidateRefUnderRepository`): applied
   field-by-field to every ref-bearing field, and re-applied to rollback
   journals, because a journal is itself a signer-boundary artifact that
   drives tag deletion.

Digest checks prove what the image is. Repository confinement proves where a
mutation is allowed to land. Neither implies the other.

### The promotion journal

`PromotionRecord` captures exactly the state that cannot be reconstructed from
the ledger afterwards: whether the immutable final tag already existed, and
what digest the moving tag pointed at before. It is restricted to the release
stage with entry release tags, because that is the only case with that
property. The JSON Lines journal is written before any tag moves. A truncated
line is refused, and a retry requires records matching the complete intended
promotion. After a partial promotion, a retry reuses that complete matching
journal byte-for-byte rather than replanning from the changed registry state
and overwriting the original rollback evidence. Journals carry an explicit
version; legacy unversioned journals are refused because their absence fields
may have come from failed registry reads. Rollback without a current
pre-promotion journal is refused because current tag state cannot prove which
run created a tag.

`SignatureCopier` being nil refuses cross-repository promotion outright rather
than completing it without signatures.

### Base-image signing

`container base-images sign` is the signer side of the base-image lifecycle:
it admits a ledger of `image_kind: base` entries, builds the run-level
provenance, logs in, and signs and attests each image. Its help lists the
steps. What ties it to the rest of the lifecycle is that it records what the
verifiers later expect:

| The signer records | from | A verifier matches it with |
|---|---|---|
| the base-image repository every ref and tag stays inside | `--expected-repository`, or the forge host, the lowercased `--repository` and `--repository-suffix` | the same flags |
| the provenance source | `<server-url>/<repository>` | `--expected-source`, or the same two flags |
| `externalParameters.workflow` | `--workflow` (required) | `--expected-workflow` |

`--workflow` and `--expected-workflow` answer to one rule: a relative path with
no `..` step and no control character. The recorded `gitCommit` is
`--source-sha`, a flag because a signer can run on another commit than the one
the base images were built from; the builder identity, the invocation and the
ref are the runner's own.

The signed statement says the images were built from `--repository`, so
`sign` writes only where that repository's owner does. The base-image
repository, derived or named with `--expected-repository`, must be:

- on the registry the command logs in to, host and port exactly. That is
  `--registry`, or without it the host of `--server-url`: the one place the
  engine maps a forge to its registry. A forge whose registry has its own
  host, as GitLab's does, is named with `--registry`;
- below the owner namespace of `--repository` (everything before its last
  path element), compared without case.

`--expected-repository` therefore chooses the repository name below the owner
and nothing else. `forge.example/owner/any-name` is accepted for
`--repository Owner/App`; another owner, an owner that only starts the same,
another host or another port is refused before the ledger, a credential or the
key is read, and no flag allows it. `promote` writes the final tags of the same
repository and holds it to the same rule. `validate`, `cleanup` and `prune` sign
nothing and promote nothing, and `cleanup` and `prune` also serve base images
kept on a registry beside the runner (`--local-registry`), so they take the
repository as named.

The engine cannot prove that a host given with `--registry` belongs to the
forge at `--server-url`. A workflow that signs for callers it does not trust
leaves `--registry` and `CONTAINER_REGISTRY` unset, which pins the registry to
the forge's own host.

The caller still provides the ledger file and the SBOM files its entries name.

### Base-image retention

`cleanup` handles staging tags, not promoted final tags. Manifest candidates
use `staging-<base-input-id>-<flavor>`; architecture candidates use
`staging-<content-id>-<flavor>-<arch>` and the caller-supplied mapping to a final
base. There is no age cutoff. Deletion requires positive final-tag evidence;
unknown architecture mappings and missing finals in the sweep are preserved.
A permission refusal is logged and the tag preserved without failing cleanup,
so successful completion does not mean every staging tag was removed. Promoted
bases remain for the separate retention pass.

`container base-images prune` derives a keep-set from the **signed SLSA
attestations** of the release refs supplied through `--release-images`.
The caller must supply the complete policy-selected set of supported releases,
using recorded immutable digests rather than resolving moving tags at pruning
time. The command verifies supplied refs but cannot detect an omitted release.
The predicate records `base.input_id` alongside the image; the internal
`release-images.json` ledger is not the retention input.

The supplied public-key file is the trust root. A configured
`--cosign-public-key-sha256` pin is checked before external calls. Optional
`--expected-source` and `--expected-workflow` constrain the release attestations,
not the base builder; neither is inferred from the base repository. Omission
allows releases from multiple consumers signed by that key. An explicit
`--auth-file` is used for both attestation verification and OCI registry access.

Retention can affect future rebuilds as well as re-verification of existing
releases. Rebuilding a missing base is consumer-owned and depends on continued
availability of its inputs and tools. The following guards limit a pass:

| Situation | Behaviour |
|---|---|
| Attestation does not verify, or names no base | **abort**; under-counting the keep-set deletes a live base |
| Nonempty inventory but empty keep-set | **refuse**; an empty inventory with an empty keep-set is a no-op |
| More unreferenced base-input IDs than `--max-delete` | **refuse**; the bound counts IDs, not tags. Every flavor tag of an eligible ID is selected; `0` disables the bound |
| Default invocation | **`--dry-run`**; read the list, then pass `--dry-run=false` |

`--local-registry` runs the same pass against a plain OCI registry, for bases
kept beside the runner rather than pushed to a remote package host. Run it on a
schedule, never in the release path: retention failing must not fail a release.

### Ports

The registry surface is decomposed to the minimum each operation needs:
`DigestResolver`, `Registry` (resolve plus copy), `TagDeleter`,
`CleanupRegistry`, and `PromotionRollbackRegistry`. Tag deletion routes
through the forge package API rather than a generic OCI delete, because
staging and final tags share one manifest and a naive OCI delete would take
the release tag with it.

The `--local-registry` path retains the configured registry host and sends
tag-addressed OCI DELETE requests. It resolves the tag's digest and refuses
known sharing with another tag as a conservative precheck. That listing is a
snapshot, not a lock against concurrent publication; the DELETE still names
the tag. The registry must support that operation. There is no fallback to
deleting by manifest digest.

## How each flow binds build to signature

The protocols below apply when build and signing are isolated into separate
jobs, as in the Forgejo release flow. That separation must be established by
the workflow. The default GitHub container workflow attests per-architecture
SBOMs in its build job and signs the merged image in its publishing job; its
[SLSA scope](verification.md#container-image-attestation--verification) is
documented separately.

The **image flow** leans on the registry. A registry is content-addressed and
independent of the CI run, so the signer can simply re-resolve the digest from
it using a release tag the signer controls. The evidence lives outside the
pipeline, and the ledger is only a claim about it.

The **isolated artifact flow** has no such registry. The forge's run-artifact
store is the transport itself and is not content-addressed. A digest recorded
beside the artifact would sit in the same trust domain as the bytes it
describes and would prove nothing. The hand-off therefore carries the expected
value out of band, over the forge's control plane:

1. The producing job computes the digest with `release dist-digest` and emits
   it as a job output.
2. The caller passes that output into the signing workflow as an input.
3. The signing job re-derives the digest of what it downloaded and compares,
   with `release validate-dist --dist-dir … --expected-digest …`, which also
   rejects a `dist/` containing symlinks, non-regular entries, or control
   characters in a path.

The expected digest must not travel inside the artifact it protects. Under the
forge control-plane trust assumption, rewriting the artifact does not rewrite
the already-emitted output of a completed job. Comparing against that output
binds the downloaded bytes to the build output; it does not establish that the
build itself was trustworthy.

`validate isolation` checks selected static declarations, not execution of this
protocol or SLSA Build L3 compliance. With the corresponding flags enabled it
checks the build output declaration and supported `needs` output-reference
spellings at the signing call site, release identity/secret wiring, and selected
checkout/cache rules. These are bounded static checks, not universal workflow
expression evaluation or a proof that the channel is intact at runtime.

For an identified `actions/checkout` step, `persist-credentials` must resolve
through YAML aliases/merges to the canonical literal `false`, bare or quoted.
Missing, true, expression-valued (including `${{ false }}`), and uninspectable
values refuse; an input decode failure must not hide the checkout step. This is
a static input requirement, not evaluation of checkout expressions.

If a local sibling signer workflow is available, the checker compares its
`workflow-call-secrets-contract` header when present and flags an absent literal
`inputs.dist-digest` reference (including the supported quoted-bracket spellings).
That reference scan is explicitly a lexical hint: a name or description can
satisfy it without consuming the input. It does not verify that checkout against
the pinned revision. An absent sibling is explicitly reported as not checked.
Workflow names, descriptions, comments, and arbitrary scalar substrings are not
evidence that a digest verifier runs or precedes signing.

The **owner of the pinned signing workflow** must enforce
the runtime verifier, trusted expected-digest input, refusal on verification
failure, and ordering before any signing. On Forgejo that workflow is this
repository's `.forgejo/workflows/sign-and-publish-release.yml`, whose guards in
`test/forgejoguard` hold the order. The engine's
`VerifyDist`, exposed as `release validate-dist`, still performs the real
structural and digest checks when invoked. A static isolation pass neither
invokes it nor proves it is invoked. These security requirements remain
necessary; their enforcement belongs
to the thin workflow/typed-engine integration, not a shell interpreter in this
validator.

Signing-secret diagnostics cover configured literal dot and quoted-bracket
members inside `${{ ... }}`, not dynamically computed secret names or all
expression semantics. Effective YAML mappings honor aliases, direct-key
overrides, and first-source merge precedence; job env overrides workflow env.
Each distinct matched secret in a containing scalar is reported with its
consuming job and the scalar's starting source line (the anchor definition for
an alias). Repeated tokens in one scalar are not separate diagnostics, and
folded/escaped strings do not promise exact token-line source maps. Cyclic or
over-limit secret traversals refuse rather than silently passing.

Two consequences worth knowing:

- The forge's run-artifact store is still a trusted component for availability
  and for flows that do not bind, and it is named in
  [the threat model](threat-model.md#trust-boundaries).
- forge-ci's own GitHub release builds and signs in a single job, taking its
  provenance subjects from the checksums produced there, so it has no cross-job
  hand-off to bind in the first place.

`forge-ci artifact digest` is a separate primitive with a different scheme:
it commits to each file's execute bit and size, which the sha256sum-compatible
`dist-digest` cannot. It is available to adopters who want a strict content
digest inside their own flow. It is not the hand-off mechanism; reach for
`release dist-digest` for that.
