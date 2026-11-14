# Example: the signer image and engine binary on Forgejo

This directory is for the organization that serves the Forgejo layer, not for
a consumer. The consumer callers live with their ecosystem:
[Go CLI](../go-cli/forgejo/), [Go service](../go-service/forgejo/) and
[Cargo](../cargo-app/forgejo/); see the [examples index](../README.md).

[`signer-image.yml`](signer-image.yml) is the main file here. One repository
of your organization runs it, at the commit of `forge-ci` your consumers pin.
Its first job publishes the layer's binary for that commit to your instance,
without which no job of the layer runs. The others build and publish the
signer image, for linux/amd64 and linux/arm64, and output the digest-pinned
reference every consumer `release.yml` passes as `signer-image`. Its image
builds need user namespaces: on a pool that denies them, a `request-runner`
job borrows a runner, or the builds use kaniko; see
[Runners and runner lending](../../docs/runners.md).

[`kaniko-job-image.yml`](kaniko-job-image.yml) is for the kaniko case only:
it publishes the job image kaniko builds run in, from kaniko's own debug
image, and prints the digest to pin as `CI_IMAGE_BUILDER_JOB_IMAGE` with
`CI_IMAGE_BUILDER=kaniko`.

[`runtime-images.yml`](runtime-images.yml) is an opt-in runtime publisher for
a Forge CI mirror, sharing upstream's Containerfile and smoke checks. Replace
its example forge URL and `FORGE_CI_COMMIT` with the mirror's published immutable
engine revision. Configure the image namespace, reviewed digest-pinned signer
and Go build images, native runner pools, registry credentials and public-key
digest as described in [Runtime images](../../docs/runtime-images.md#configuration).

[`package-retention.yml`](package-retention.yml) previews a separately reviewed
consumer-pin matrix using that namespace and registry account. It never applies
deletion. Copy either example into `.forgejo/workflows/` only after configuration.

## Before it runs

The file carries placeholders, all literal so that `git grep` finds them:

| Placeholder | Replace with |
|---|---|
| `OWNER` | who hosts `forge-ci` on your instance |
| `PIN_FORGE_CI_SHA` | the `forge-ci` commit your consumers pin |

Then set, in that repository:

- the variables `CI_RUNNER_LABEL` and `CI_RUNNER_LABEL_ARM64`, the amd64 and
  arm64 runner pools as JSON label arrays such as `["ci-builder"]`, and
  `SIGNER_REGISTRY_USER`, the user of the signer image registry;
- the secret `ENGINE_PACKAGE_TOKEN`, a token that may write packages of
  `OWNER`, for the binary, and the secret `SIGNER_REGISTRY_TOKEN` for the
  image.

`OWNER`'s packages must be readable without credentials, as its `forge-ci`
repository is. The file's header lists every variable and secret, including
the ones for kaniko and runner lending. The
[Forgejo guide](../../docs/forgejo.md#the-signer-image) describes the image,
and [Forks](../../docs/forgejo.md#using-the-layer-from-a-fork) the order of
the steps around it.
