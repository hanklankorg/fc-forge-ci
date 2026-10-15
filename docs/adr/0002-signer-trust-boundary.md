# ADR 0002: The signer trust boundary

Status: Accepted

## Invariant

Consumer-controlled code or data must not introduce executable behavior or
expand publication scope inside a trusted signing job.

## Rules

1. **Consumer code executes in key-free jobs.** The signer consumes structured
   ledgers, predicates and SBOMs as data and runs only its selected trusted tools.
2. **The signer validates its inputs independently.** Build-side validation does
   not replace signer-side ledger, path, digest and repository admission.
3. **Publication scope is explicit.** Trusted workflows supply exact expected
   image/base repositories. Generic ledger commands accept caller-supplied
   constraints; registry login alone does not define that policy.
4. **Provenance extras cannot override computed facts.** External parameters may
   add fields but collisions with computed source, digest or identity fields fail.
5. **Transparency policy is explicit.** `sign.transparency` defaults to `public`.
   Keyless signing requires its transparency evidence; `method=sigstore` with
   `transparency=none` is rejected. Key-backed signing can use a no-log policy.

`cosign.New` and `cosign.NewIsolated` resolve
`FORGE_CI_COSIGN_TRANSPARENCY`. The same policy controls signing and verification,
so a no-log signature is verified with the corresponding no-log requirements.

## Environment and network scope

An `env://` signing key selects an isolated Cosign environment containing its
named key, passphrase, runtime settings and explicitly allowed registry inputs.
File, KMS and keyless references retain the authentication context those
backends need. Proxy URLs can contain credentials and are part of that context.

`transparency=none` controls public Sigstore service use. The adapter supplies
a no-log signing configuration and local trust material to avoid public Rekor
publication and default public TUF lookups. Registry and KMS operations can still
require network access; the policy does not mean that signing is offline.

## Ownership

Callers may compute their own build graphs and emit ledgers. Signer-side
validation, signing and publication decisions stay inside the trusted engine
and pinned policy. Changes to this boundary are reviewed together with the
signer tooling, workflow scopes and byte-verification mechanism.

See [the threat model](../threat-model.md), [verification](../verification.md),
and [open questions](../open-questions.md) for current guarantees and limitations.
