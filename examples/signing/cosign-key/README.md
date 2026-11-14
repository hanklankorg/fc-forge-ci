# Cosign key signing example

A Go CLI released with signatures made by a Cosign key pair the organization
holds as secrets, the way the GitLab and Forgejo layers sign. One key pair can
serve an organization on all three forges.

This example sets `sign.transparency: none`, so signing contacts no public
Sigstore endpoint and writes nothing to the public Rekor log. A signature is
verified against the public key alone.

What this example adds is the `sign:` block of `artifacts.yml` and the
`secrets:` and `cosign-public-key*` lines of `github/release-workflow.yml`:
copy the block into your `.forge-ci/artifacts.yml` and the lines into your
release workflow. The rest of both files is the [Go CLI](../../go-cli/)
example.

## One-time setup

```bash
# 1. Create the key pair; cosign asks for the passphrase.
cosign generate-key-pair

# 2. Store the private half and its passphrase as secrets.
gh secret set RELEASE_COSIGN_PRIVATE_KEY < cosign.key
gh secret set RELEASE_COSIGN_PASSPHRASE

# 3. Publish the public half as a variable, and record its digest.
gh variable set COSIGN_PUBLIC_KEY < cosign.pub
sha256sum cosign.pub
```

Write the digest into `cosign-public-key-sha256` in
[`github/release-workflow.yml`](github/release-workflow.yml). Keep `cosign.key` offline, or
delete it once the secret is set.

## What the layer does with it

- `sign.key: env://COSIGN_KEY` is the one environment reference
  `artifacts.yml` accepts. Another variable is refused: Cosign would receive
  it.
- The two secrets reach only the steps that sign with them: the release's
  artifact signing and the container image signing and attestation. A run
  configured another way never holds them.
- Each job that holds the key first runs `forge-ci validate signing-key`
  against the public key and its digest, and signs nothing on a mismatch.

## Verifying a release

```bash
cosign verify-blob --key cosign.pub --insecure-ignore-tlog \
  --bundle my-cli-linux-amd64.bundle my-cli-linux-amd64
cosign verify --key cosign.pub --insecure-ignore-tlog ghcr.io/OWNER/my-cli:v1.2.3
```

See [Signing](../../../docs/github.md#signing) and
[Verification](../../../docs/verification.md).
