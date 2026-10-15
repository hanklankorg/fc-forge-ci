# OpenBao (KMS) signing example

A Maven library released with signatures produced by OpenBao's
Transit secrets engine. The private key lives in OpenBao and never
leaves it; cosign sends the artefact hash to OpenBao for signing.

This example sets `sign.transparency: none`, so signing does not contact
Sigstore's public Fulcio, Rekor, or TUF endpoints. It is suitable for a
private-only signing network, but the release is not offline: the runner must
reach OpenBao plus the forge, package registry, and container registry used by
the release.

The generic reusable release workflow does not perform a provider-specific
OpenBao JWT exchange. An organization-owned wrapper or fork must exchange the
GitHub OIDC JWT at OpenBao's auth endpoint and expose the resulting short-lived
`VAULT_TOKEN`, together with `VAULT_ADDR`, to the cosign steps. The OIDC JWT
therefore leaves the runner for OpenBao; the private signing key does not leave
OpenBao.

## One-time OpenBao setup

```bash
export BAO_ADDR=https://bao.examplescope.internal
bao login

# 1. Enable Transit
bao secrets enable transit

# 2. Create the signing key (ECDSA-P256 — broadest cosign compat)
bao write -f transit/keys/release-signing \
    type=ecdsa-p256 \
    exportable=false \
    allow_plaintext_backup=false

# 3. Export the public half and commit it
bao read -format=json transit/keys/release-signing \
    | jq -r '.data.keys."1".public_key' > .forge-ci/release-pubkey.pem
git add .forge-ci/release-pubkey.pem
git commit -m "chore: add OpenBao release signing pubkey"

# 4. Enable JWT auth so CI can authenticate via the GHA OIDC token
bao auth enable jwt
bao write auth/jwt/config \
    oidc_discovery_url=https://token.actions.githubusercontent.com \
    bound_issuer=https://token.actions.githubusercontent.com

# 5. Create a policy + role bound to this repo + release tags
bao policy write release-signing - <<EOF
path "transit/sign/release-signing" { capabilities = ["update"] }
EOF

bao write auth/jwt/role/release-signing - <<EOF
{
  "role_type": "jwt",
  "user_claim": "sub",
  "bound_audiences": ["https://bao.examplescope.internal"],
  "bound_claims": {
    "repository": "examplescope/myorg",
    "ref_type": "tag"
  },
  "policies": ["release-signing"],
  "ttl": "10m"
}
EOF
```

## Files

- `artifacts.yml` declares `sign.method: kms`, the
  `hashivault://transit/keys/release-signing` URI, and
  `sign.transparency: none`.
- `github/release-workflow.yml` shows the caller permissions and documents the wrapper
  step the organization must add for OpenBao authentication.

## What gets produced

This example selects KMS signatures with no public transparency log.
The shared [verification reference](../../../docs/verification.md#signtransparency-what-the-signing-run-publishes-about-itself)
owns the Sigstore v3 bundle, OCI-signature and absent-transparency-proof contracts.
Use the example's committed public key in the concrete commands below.

## How to verify

Artefacts:

```bash
forge-ci validate artifact-signature \
    --artifact my-lib-1.0.0.jar \
    --key .forge-ci/release-pubkey.pem
```

Container image:

```bash
forge-ci validate container-signature \
    ghcr.io/examplescope/myorg/my-lib@sha256:abc123... \
    --method kms \
    --key .forge-ci/release-pubkey.pem
```

Keep the committed public key and changes to the signing workflow under review.
The [verification reference](../../../docs/verification.md#--methodkms-cosign-with-explicit-key-reference)
owns the general KMS trust model; this example supplies its specific Transit key,
JWT role and caller configuration.
