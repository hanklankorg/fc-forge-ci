# Sigstore-keyless signing example

A Go CLI whose release artifacts use Sigstore-keyless signatures via cosign.
There is no long-lived **artifact-signing** key: the artifact identity is the
GitHub Actions workflow that produced the release.

Release commits and tags are signed independently. This example sets
`git-signing.method: ssh`, so it still needs `RELEASE_SSH_SIGNING_KEY` and a
matching principal and public key in `.forge-ci/allowed_signers`. Start from
the included `.forge-ci/allowed_signers.example`, replace its demonstration
key with the public half of `RELEASE_SSH_SIGNING_KEY` and its principal with
the `bot-email` you set in `github/release-workflow.yml`, and commit it as
`.forge-ci/allowed_signers`. The SSH key protects Git objects; it is not used
for artifact signatures. Git objects cannot be signed keyless.

## Files

What this example adds is the `sign:` and `git-signing:` blocks of
`artifacts.yml` and the `secrets:` lines of `github/release-workflow.yml`:
copy the blocks into your `.forge-ci/artifacts.yml` and the lines into your
release workflow. The rest of both files is the [Go CLI](../../go-cli/)
example, which ships this setup.

- `artifacts.yml` declares `sign.method: sigstore` and
  `git-signing.method: ssh`.
- `github/release-workflow.yml` is the caller workflow with the required
  `id-token: write` permission so cosign can mint the runner-issued
  OIDC token, and `RELEASE_SSH_SIGNING_KEY` as its one signing secret.
- `.forge-ci/allowed_signers.example` shows the OpenSSH allowed-signers
  format required to verify release commits and tags. Its bundled key is only a
  demonstration key and its principal a FIXME placeholder; replace both.

## What gets produced

Each release binary gets a Sigstore bundle, `<file>.bundle`, with the
signature, the Fulcio certificate for the workflow identity and the Rekor
proof. If you add a container, its signature is stored in the registry next
to the image. See
[what the signing run publishes](../../../docs/verification.md#signtransparency-what-the-signing-run-publishes-about-itself).

## How to verify

Artefacts (a downloaded `.bundle` and the matching file):

```bash
forge-ci validate artifact-signature \
    --artifact my-cli-linux-amd64 \
    --cert-identity-regexp '^https://github.com/examplescope/myorg/\.github/workflows/release-workflow\.yml@refs/tags/release-request/v.+$' \
    --cert-oidc-issuer https://token.actions.githubusercontent.com
```

Container image:

```bash
forge-ci validate container-signature \
    ghcr.io/examplescope/myorg/my-cli@sha256:abc123... \
    --method sigstore \
    --cert-identity-regexp '^https://github.com/examplescope/myorg/\.github/workflows/release-workflow\.yml@refs/tags/release-request/v.+$' \
    --cert-oidc-issuer https://token.actions.githubusercontent.com
```

What this proves: "the file was signed by a job running
release-workflow.yml in examplescope/myorg on a release-request tag". The
branch-protection rules on that workflow file become the
artifact-signing security posture: no reusable artifact-signing private key
exists. Protect and rotate the separate SSH Git-signing key normally.

## Switching from GPG

Follow the [artifact-signing migration guidance](../../../docs/verification.md#changing-artifact-signing-method)
when changing existing consumers from GPG to this example's Sigstore bundles.
