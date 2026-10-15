# Example: the signer image and engine binary on Forgejo

This directory is for the organization that serves the Forgejo layer, not for
a consumer. The consumer callers live with their ecosystem:
[Go CLI](../go-cli/forgejo/), [Go service](../go-service/forgejo/) and
[Cargo](../cargo-app/forgejo/); see the [examples index](../README.md).

[`signer-image.yml`](signer-image.yml) is the one file here. One repository
of your organization runs it, at the commit of `forge-ci` your consumers pin.
Its first job publishes the layer's binary for that commit to your instance,
without which no job of the layer runs. The others build and publish the
signer image, for linux/amd64 and linux/arm64, and output the digest-pinned
reference every consumer `release.yml` passes as `signer-image`.

## Before it runs

The file carries placeholders, all literal so that `git grep` finds them:

| Placeholder | Replace with |
|---|---|
| `OWNER` | who hosts `forge-ci` on your instance |
| `PIN_FORGE_CI_SHA` | the `forge-ci` commit your consumers pin |

Then set the variable `CI_RUNNER_LABEL` and the secret
`RELEASE_REGISTRY_TOKEN` in that repository: a token that may write packages
of `OWNER`, for both the image and the binary. `OWNER`'s packages must be
readable without credentials, as its `forge-ci` repository is. The
[Forgejo guide](../../docs/forgejo.md#the-signer-image) describes the image,
and [Forks](../../docs/forgejo.md#using-the-layer-from-a-fork) the order of
the steps around it.

The tests in `test/forgejoguard` and `test/forgejobehavior` hold this file to
the layer: it publishes the binary first and builds from one verified
checkout.
