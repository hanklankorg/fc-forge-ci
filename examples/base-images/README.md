# Base images

A project that ships a graph of base images: one root, `base`, and two
flavors built on it, `tools` and `debug`. The project owns the graph: which image starts from
which, what goes into an image's input ID, and the content-addressed tags.
forge-ci builds one stage of it at a time, the root and then its flavors,
and holds each image to the same rules. The flavors are siblings, so one
plan lists them and the same calls build, sign and promote them all.
The example is for Forgejo and GitLab.

A flavor builds only on a parent that was first verified against your Cosign
key, and the build jobs hold no registry token or signing key. The trust
rules are in the [Forgejo](../../docs/forgejo.md#base-images) and
[GitLab](../../docs/gitlab.md#base-images) guides.

## Files

| File | Copy to |
|---|---|
| `artifacts.yml` | `.forge-ci/artifacts.yml` |
| `base-plan.sh` | `base-plan.sh` |
| `images/` | `images/` |
| `forgejo/release.yml` | `.forgejo/workflows/release.yml` |
| `gitlab/.gitlab-ci.yml` | `.gitlab-ci.yml` |
| `gitlab/check-release.sh`, `gitlab/build-release.sh` | `.gitlab/` |
| `nanolinter.toml` | `nanolinter.toml` |

`base-plan.sh` writes the plan of one stage: the root, or the flavors named
as children of one parent. It is ordinary project code: replace it with your
own planner when the graph grows, as long as each plan is one complete stage
in the shape
[`base-images.yml`](../../.forgejo/workflows/base-images.yml) describes. Both
forges call it the same way.

## Forgejo

A signed `release-request/vX.Y.Z` tag starts the release. It builds, signs and
promotes `base`, authenticates it, then builds, signs and promotes `tools`
and `debug` on it as one stage, and publishes a release whose signed file,
`image-inventory.json`, names all three images by digest. See [base images](../../docs/forgejo.md#base-images).

A further flavor of `base` is one more name in the flavors' plan. A flavor of
a flavor is a further stage: `base-images.yml`, `sign-container-images.yml`
and `promote-base-container-images.yml`, after a
`verify-base-container-images.yml` call that authenticates its parent.

## GitLab

`gitlab/.gitlab-ci.yml` is the merge-request gate: lint, and the planner's
own check. The release runs from your organization's release policy project, which
names the graph and the planner beside its other inputs:

```yaml
base-stages: base tools+debug:base
base-plan: bash base-plan.sh
image-builder: buildah
builder-job-image: <the Buildah job image, pinned by digest>
checks: [bash .gitlab/check-release.sh]
build: [bash .gitlab/build-release.sh]
```

A signed `release-request/vX.Y.Z` tag starts it. The policy builds, signs
and promotes `base`, verifies it in a signer job, then builds, signs and
promotes `tools` and `debug` on that one digest. The images live in the
`bases` repository of the publication project. `build-release.sh` runs once
every stage is promoted and writes the release's signed file,
`image-inventory.json`, from what the stages hand on. See
[base images](../../docs/gitlab.md#base-images).

A further flavor of `base` is one more name in its stage,
`tools+debug+extra:base`; a flavor of a flavor is a further stage,
`extra:debug`. A stage whose images an earlier release promoted is verified
and reused, not rebuilt under a new tag.
