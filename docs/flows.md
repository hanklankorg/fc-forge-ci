# Artifact and image flows

How a thing you declare becomes a thing you release. This page explains the
two flows that carry build output through the pipeline: **run artifacts**,
which move files between jobs, and the **image ledger**, which moves container
images from build to signature to final tag.

## Orientation

Two things are called "artifact":

- the **declared artifact**: what you list in `.forge-ci/artifacts.yml`, such
  as a JAR, a binary or a container image;
- the **run artifact**: the temporary storage that carries files between the
  jobs of one run. `forge-ci artifact upload` and `download` handle it. The
  forge's retention policy decides how long it lives.

Container images do not travel as run artifacts. Each image goes to a registry
as soon as it is built, and the jobs pass a **ledger** instead
(`release-images.json`): a note that the image at digest `sha256:abc...` should
become `v1.2.3`. Where a separate job signs, the signer checks the ledger
against the registry and against the release tag it was given, so a note
asking for `v9.9.9` during the `v1.2.3` release is refused. Promotion moves
tags; it never rebuilds an image.

## How it works

### The artifact inflow

The chain is **declare, plan, build, upload, download, consume**.

1. You write `.forge-ci/artifacts.yml`. If it is absent and the repository has
   exactly one root ecosystem manifest (`pom.xml`, `go.mod`, and so on), an
   equivalent declaration is derived in memory.
2. The declaration is parsed strictly, so an unknown key or a typo fails,
   and turned into a JSON plan.
3. Every later job reads the plan, not the YAML. The plan names each run
   artifact once, so a build job and a publish job agree on its name.
4. Build jobs upload their output under those names. Downstream jobs, such as
   container build, SBOM, sign, release and publish, download them.

The transport depends on the forge:

| Forge | How run artifacts move |
|---|---|
| GitHub | One ZIP blob per artifact, in the run's artifact store |
| Forgejo | Each file on its own, through Forgejo's artifact API |
| GitLab | The runner's native `artifacts:` and `needs:` in the job YAML |

On GitHub and Forgejo, `forge-ci artifact download` refuses absolute paths,
`..`, backslash separators and symlinks, and files over 5 GiB. On GitLab the
runner extracts the files itself.

### Two kinds of image, two lifecycles

`container` groups image commands under three names:

| Group | Images | Addressed by | Lifecycle |
|---|---|---|---|
| `release-images` | what a release publishes | the release tag | staging → final + moving, with rollback |
| `base-images` | what those are built **FROM** | a content hash, `base-input-id` | staging → immutable final, plus freshness and retention |
| `ledger` | the record of the above | — | add, merge, validate |

A "base image" here is your project's own base layer, not the
`forge-ci-runtime-*` images that CI jobs run in. Its `base-input-id` is a
SHA-256 value your project computes from the inputs it chooses; forge-ci
checks that it agrees with the signed evidence but does not recompute it.

### The image ledger

The lifecycle is **add, merge, sign, promote, cleanup**, with **rollback**
beside it.

- **add**: the build job records one entry per image it pushed. `ledger add`
  either resolves the digest with `--capture-digest` or accepts an explicit
  `--ref`/`--digest` pair. Parallel build jobs can write the same file, and
  adding an identical entry again changes nothing.
- **merge**: for multi-container releases, each container's job writes its own
  small ledger and the promotion job joins them into one.
- **sign**: the trust boundary. See below.
- **promote**: retags the recorded image and checks the destinations. Never a
  rebuild. An immutable destination already serving the same digest is skipped;
  a different digest is refused. Moving tags are always set to the recorded
  digest.
- **cleanup**: deletes the temporary `staging-` tag, but only after confirming
  the promoted tags really serve the digest.
- **rollback**: undoes a promotion by replaying a journal written before
  anything moved.

Each entry names its `final_tag`, which must be the release tag or
`<releaseTag>-<suffix>`, and its `candidate_tag`, which must be exactly
`staging-<final tag>`. So a release promotes exactly the image staged for it.

CLI release archives include `docs/schemas/release-images.schema.json` from the
same source revision as the binary. Validate ledgers with that bundled schema.

A release without images passes `--allow-missing-ledger` to
`container release-images sign`, `promote` and `cleanup`: a missing default
ledger is then not an error. An existing ledger is checked in full either way;
see the [CLI reference](cli-reference.md).

#### Stage ladder and registry routing

The [image promotion ladder](workflows.md#image-promotion-ladder) moves one
recorded digest through `:dev`, `:staging` and `:release`, without rebuilding
or re-signing. With `--stage-repo <prefix>`, the destination keeps the full
source path below the registry host, so `ghcr.io/owner/api` becomes
`<prefix>/owner/api`. A copy to another repository or registry carries the
signatures and attestations with the image, and is refused when it cannot.

### The trust boundary

The same ledger check runs twice: when the build job records an entry, and
again in the signer. The build job's own check only stops honest mistakes
early, since that job controls its own inputs. The signer re-runs it with a
release tag the signer controls, not one from the build output. So a
compromised or buggy build job cannot get an image signed and promoted under
a tag outside the release it was authorized for.

## Builder-neutral execution

Every forge uses the same build checks, scans, evidence formats and
publication rules; only the job that runs the image builder differs. Buildah
is the default everywhere, and Kaniko is the alternative. On GitHub, Kaniko
runs in a disposable Docker action; on Forgejo and GitLab it runs as a job
container. A capability a builder lacks fails the job; forge-ci never
switches to another builder on its own.

How the three delivery layers compare is in
[Delivery layers side by side](providers.md#delivery-layers-side-by-side).

## Base-image retention

`container base-images cleanup` removes staging tags whose final tag is
confirmed; it never removes promoted finals and has no age cutoff. A tag it
may not delete is logged and kept.

`container base-images prune` keeps every base image that the signed
provenance of the releases you pass with `--release-images` names, and
removes the rest. Pass the complete set of releases you support, by digest:
the command checks what you pass but cannot notice a release you left out.
These guards limit a pass:

| Situation | Behaviour |
|---|---|
| Attestation does not verify, or names no base | **abort**; under-counting the keep-set deletes a live base |
| Nonempty inventory but empty keep-set | **refuse**; an empty inventory with an empty keep-set is a no-op |
| More unreferenced base-input IDs than `--max-delete` | **refuse**; the bound counts IDs, not tags. Every flavor tag of an eligible ID is selected; `0` disables the bound |
| Default invocation | **`--dry-run`**; read the list, then pass `--dry-run=false` |

Run it on a schedule, never in the release path: a failed retention pass must
not fail a release. `--local-registry` runs the same pass against a plain OCI
registry beside the runner. Tag deletion goes through the forge's package API,
because staging and final tags share one manifest and a plain OCI delete would
remove the release tag too.

## How each flow binds build to signature

The protocols below apply when build and signing run in separate jobs, as in
the Forgejo and GitLab release flows. The workflow has to set up that
separation. The default GitHub container workflow stages its builds before
publication, attests per-architecture SBOMs, then signs and attests the merged
index. Its [SLSA scope](verification.md#container-image-attestation--verification)
is documented separately.

The **image flow** relies on the registry. A registry is content-addressed and
independent of the CI run, so the signer re-resolves the digest from it using
a release tag the signer controls. The ledger is only a claim about it.

The **isolated artifact flow** has no such registry. The forge's run-artifact
store is the transport itself and is not content-addressed. A digest stored
beside the artifact would sit in the same trust domain as the bytes it
describes and would prove nothing. So the expected value travels over the
forge's control plane instead:

1. The producing job computes the digest with `release dist-digest` and emits
   it as a job output.
2. The caller passes that output into the signing workflow as an input.
3. The signing job computes the digest of what it downloaded and compares,
   with `release validate-dist --dist-dir … --expected-digest …`, which also
   rejects a `dist/` containing symlinks, non-regular entries, or control
   characters in a path.

The expected digest must never travel inside the artifact it protects. A
rewritten artifact does not rewrite the output of a job that already finished,
so the comparison binds the downloaded bytes to the build output. It does not
prove that the build itself was trustworthy.

`forge-ci validate isolation` (the `check-l3-isolation` action on Forgejo)
checks the workflow statically: the build output is passed as a `needs`
output, the signer's secrets are listed, and checkout uses
`persist-credentials: false`. It does not prove that the signer runs
`release validate-dist`; the pinned signing workflow does that.

Two consequences worth knowing:

- The forge's run-artifact store is still trusted for availability and for
  flows that do not bind, and it is named in
  [the threat model](threat-model.md#trust-boundaries).
- forge-ci's own GitHub release builds and signs in a single job, taking its
  provenance subjects from the checksums produced there, so it has no cross-job
  hand-off to bind.

`forge-ci artifact digest` is a separate command with a different scheme: it
covers each file's execute bit and size, which the sha256sum-compatible
`dist-digest` cannot. Use it for a strict content digest inside your own flow;
use `release dist-digest` for the hand-off.
