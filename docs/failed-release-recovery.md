# Failed release recovery

Use this runbook after a production release job fails. The safe default is to
preserve immutable evidence, identify the last successful mutation, and choose
the forge- and stage-specific recovery path below. Never make a failed run appear to
have succeeded by moving or force-replacing a tag.

## Simple case

Identify the failed stage before choosing a retry. Do not delete or move a
final tag, release or published image to make a retry pass. Preparation and
downstream publication have different recovery contracts:

| Failed stage / delivery path | Recovery direction |
|---|---|
| GitHub publication after successful signing | Retry the failed publisher with the original signed checkpoint, as described below. |
| GitHub preparation/prerequisites with an existing final tag | Use the [exact-tag recovery criteria](#final-git-tag-exists); a matching tag name alone is insufficient. |
| Forgejo preparation after pushing the final tag | Preparation refuses an existing final tag. Preserve successful handoffs for downstream jobs; if preparation itself cannot complete, use a new version and request. On Forgejo 17 or later, a run cancelled after the unsigned hand-off is finished in the same run by `POST /api/v1/repos/{owner}/{repo}/actions/jobs/{job}/rerun` on its first cancelled job, which reruns the jobs that need it with the earlier jobs' hand-off; held by `TestForgejoRelease_RerunAfterInterruption` (Forgejo 17 or later; not yet live-proven, see [Testing matrices](testing-matrices.md)). |
| GitLab | Retry the failed job in the same pipeline; see [GitLab](#gitlab). |

Releases start from the original release request, not a fresh event on the final
tag. A retry is supported only while the required source identities, handoffs
and destination state remain compatible with that operation.

For GitHub publication failures, retry the failed publisher job, preserving
the successful signing job. Its `release-signed-<run>-<attempt>` checkpoint is
kept for 30 days and contains the exact signed files and notes. Publication
verifies the signer's digest and rechecks the prepared tag. Do not regenerate
signatures to repair an already-published release: Sigstore bundles can differ
even when the binary is unchanged, and immutable releases refuse replacement.

## 1. Freeze and inventory

1. Stop automatic retries while the failure is classified.
2. Record the release-request tag and commit, intended final tag and release bump
   commit (if created), workflow run, assembly manifest, image ledger, and
   promotion journal.
3. Check which external mutations exist: final Git tag, forge release, uploaded
   assets, package versions, container version tags, moving container pointers,
   and transparency-log entries.
4. Preserve logs and manifests. They are recovery inputs, not disposable build
   output.

Do not expose secret values while collecting evidence. Credential presence and
provider error classes are sufficient for the inventory.

## 2. Choose the recovery path

### No external mutation

Fix the prerequisite or build failure and rerun the same release-request tag at
the same commit. Use the snapshot flow for branch testing; changing a stable
request to a prerelease is not recovery of the original version.

### Final Git tag exists

Verify that the remote final tag resolves to the intended release commit.

- On GitHub, a matching final tag can be reused only if all exact-tag recovery
  criteria below hold. Retry from the same release request, not from the final tag.
- Forgejo's prepare guard refuses an existing final tag. Retry downstream jobs
  from successful preparation handoffs, or use a new version if preparation
  cannot complete; do not remove the tag to bypass that guard.
- On GitLab, the request check and preparation refuse an existing final tag;
  see [GitLab](#gitlab).
- If it differs, stop. Do not force-move or delete the tag. Choose a new version
  after investigating how the conflicting immutable tag was created.

An existing tag is not sufficient evidence by name alone; the commit identity
must match exactly. GitHub's release prerequisites enforce this. A final tag is
reused only when its release bump commit sits directly on the commit the
release-request tag names, is the branch head, and verifies with the release
key.

### Forge release or assets are partial

Keep the final tag immutable. Where the selected publisher supports a retry,
use the same commit, version and signed handoff. Before retrying, verify that any
existing asset with the same name has the expected digest. A conflicting asset
is evidence of a non-idempotent or compromised release and requires a new
version, not silent replacement.

### A package registry accepted the version

Treat an externally published package version as consumed. Do not overwrite it,
even if the forge release later failed. Complete any safe same-version steps
that the registry supports as idempotent; otherwise fix the cause and issue a
new patch version. Registry-specific deletion is not a normal rollback path.

### Container promotion partially completed

Use the promotion journal as the authority. `container ledger rollback` may
restore moving pointers from that journal; do not infer a previous digest from
tag names or logs. Immutable version tags remain evidence and are not deleted as
part of pointer rollback.

The journal is kept with the run for 7 days, also when the promotion failed:

- GitLab: the `Publish: images` job's artifact,
  `_release-promote/image-promotions.jsonl`.
- Forgejo: the run artifact `release-promote-<attempt>` of `Publish: images`,
  holding `image-promotions.jsonl`.

It belongs to its attempt. A retry writes a journal for the registry state it
finds, so a rollback takes the first failed attempt's journal. It holds image
references, tags and digests, and no credential.

After a successful forward retry or journal-backed rollback, use the ledger
cleanup operation for staging tags. Cleanup is tag-scoped and must not delete a
manifest still referenced by a release tag.

## 3. Signing failures

Artifact signing fails unless every selected assembly asset exists and every
configured backend produces its advertised non-empty sidecar. A failed signing
run is not publishable. Remove only local staging output before retrying; do not
delete transparency-log entries or alter an already-published Git tag.

Artifact signing credentials and Git-object signing credentials are separate.
For example, repairing a keyless Sigstore failure does not repair a missing SSH
or GPG key needed for the release commit and tag.

## 4. Retry checklist

- The release-request tag still identifies the original request commit.
- Any final tag satisfies the selected forge's [recovery contract](#final-git-tag-exists);
  GitHub preparation recovery additionally checks the exact signed bump commit.
- The version is admitted by the original trusted `release-policy`: stable by
  default, or an explicitly permitted numbered prerelease.
- Existing assets and package versions do not conflict by digest/content.
- The assembly, checksums, signatures, SBOMs, ledger, and journal are bound to
  the intended release commit and their originating workflow attempts.
- Required credentials are present for the specific signing and publish
  backends in the resolved plan.
- Cleanup or rollback commands are driven by validated manifests/journals, not
  reconstructed shell arguments.

## 5. Escalate instead of retrying

Stop and investigate when any immutable identity disagrees: tag-to-commit,
asset-to-digest, package version-to-content, ledger entry-to-image digest, or
signature-to-publisher identity. Repeated retries cannot safely reconcile those
states. Preserve the evidence and release a new version only after the cause is
understood.

## GitLab

A GitLab release runs in one child pipeline, started only by the push of a
protected `release-request/vX.Y.Z` tag; a pipeline run by hand on the tag
does not release. The `release` resource group lets one release run at a
time. Jobs hand their files on as artifacts that expire after one day, so
retry a failed job within a day, in the same pipeline, with GitLab's
**Retry**. A job that reads or builds retries itself once when the engine
reports a dependency unavailable; every job that pushes, signs, tags or
publishes runs once and waits for you.

| Failed job | What exists | What to do |
|---|---|---|
| A `Check:` job, `Prepare: changelog` or `Prepare: version <family>` | Nothing outside the pipeline. | Fix the cause and retry the job. To start again, delete the request tag and push a new signed one at the head of the release branch. |
| `Prepare: release commit and tag` | Nothing, or the release commit and the final `vX.Y.Z` tag, if the push went through. | If the final tag does not exist, fix the cause and retry the job. If it exists, the release cannot be finished: the later jobs need this one and never ran, and both a retry and a new request refuse an existing final tag. Leave the tag in place and release a new version. |
| `Base: plan`, `Base: parent`, `Base: build` or `Base: all promoted` | The release commit and final tag, and the bases of any stage already promoted. | Retry the job. |
| `Base: publish` | The above; unsigned candidate bases may be in the publication project's `bases` repository, pushed with the release registry credential. | Retry the job. |
| `Base: sign and promote` | The above; some or all of the stage's final base tags may exist. | Retry the job. If every final tag of the stage exists and verifies, the stage is reused. If none exists, the job signs and promotes the stage. If only some exist, it signs nothing and promotes again, which completes the stage only when every candidate already carries a signature that verifies and each existing final tag is the digest this stage staged; otherwise it refuses before a tag moves. |
| A `Build:` or `Images:` job | The release commit and final tag; after `Images: publish`, candidate images under `staging-<final tag>` in this project's registry. | Retry the job. Each of these jobs checks the release commit again first. |
| `Sign: files and images` | The above, and images copied into the publication project under candidate tags. | Retry the job; it starts again from the same hand-off. |
| `Publish: images` | Some final image tags may have moved. The job's promotion journal is kept as an artifact for 7 days, also on failure. | Retry the job, or roll back with the first failed attempt's journal: `forge-ci container ledger rollback --journal image-promotions.jsonl --tag vX.Y.Z`, with the publication project's registry credentials. See [Container promotion partially completed](#container-promotion-partially-completed). |
| `Publish: release` | Release files uploaded to the project, or the complete GitLab release. The job uploads every file first, then creates the release with all its links in one request. | Retry the job. It accepts an existing release only when its name, notes and every file match, and refuses to replace one that differs; nothing is updated in place. |
| `Publish: npm packages` | Some packages may be published. | Retry the job: it leaves a version whose registry digest is the tarball's and publishes the rest. Other content under the same version fails it. |
| `Publish: tidy up` | The release is complete; candidate tags remain in the publication project. | Retry the job. |

When a hand-off has expired, or the final tag exists but a job before it
cannot complete, the release cannot be finished: leave the final tag in
place and release a new version. A failed pipeline rolls nothing back by
itself, and base images promoted by `base-stages` are not rolled back with a
failed release.
