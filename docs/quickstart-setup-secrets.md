# Quickstart: set up release signing secrets on Linux

Prepare SSH, Cosign and OpenPGP keys for a key-based Forge CI release. The names
below are the reusable workflows' secret names; a consumer can map its own
secret names to them. Configure only the secrets your chosen delivery layer
and release features require. See [Secrets and permissions](reference.md) and
the [Forgejo secret contract](forgejo.md#secrets-and-tokens).

SSH, OpenPGP and Cosign key secrets accept the armored key or its standard
Base64 encoding. Base64 is an encoding, not encryption. Keep public-key trust
inputs and their pinned digests in the exact format their workflow declares.

This guide creates keys locally. It does not grant repository permissions,
configure a runner, publish a release, or create API/registry tokens.
For a local inventory of release credential names and caller mappings, run
`forge-ci doctor --requirements`; see [coverage and limitations](setup-requirements.md).

**Short path:** in your repository, `forge-ci setup secrets` lists the
secrets your release workflows need, what each must allow, and the command
that stores it; `forge-ci setup signer` adds a public key to the allowed
signers. Use this guide for the keys those secrets hold.

## Prefer non-exportable keys or short-lived signing identities

For a production setup, prefer these over copying long-lived private keys
into CI:

- **Hardware-backed keys:** a hardware token, HSM or KMS key that cannot be
  exported. A hardware token that asks for presence or a PIN suits the person
  who signs release requests.
- **Sigstore keyless signing:** short-lived certificates from **Fulcio** for
  the forge's workload identity, logged in **Rekor**. Public Rekor entries are
  permanent and show the signing identity.

What each forge supports: the GitHub layer offers keyless artifact signing,
and the CLI can sign with KMS or PKCS#11. The Forgejo workflows sign with
organization-held keys and take no KMS input; public Fulcio does not trust a
Forgejo issuer. On Forgejo, `transparency-log: public` logs signatures made
with the Cosign key; it does not switch to keyless signing. Neither approach
makes an untrusted release workflow safe.

See [Choosing a method](verification.md#choosing-a-method),
[Forgejo signing](forgejo.md#signing) and the
[OpenBao example](../examples/signing/openbao-kms/README.md).

## 1. Prepare a private directory

Use a trusted Linux machine and an ordinary user account. Install OpenSSH's
`ssh-keygen`, GnuPG's `gpg`, and a verified Cosign 3 release through your normal
trusted installation process.

Replace `/home/operator` with your own home directory in every command, and
replace the example bot identity with your organization's identity. Use a fresh
directory outside any Git checkout. If it already exists, review and reuse the
existing keys or choose another directory; do not overwrite them casually.

```bash
umask 077
mkdir -p /home/operator/.local/share
mkdir -m 700 /home/operator/.local/share/release-keys
```

For an existing signing identity, recover its private key from its protected
backup instead of generating a replacement. A public key alone cannot recover
the private key.

## 2. Create the SSH release-bot key

The current secret-based SSH automation has no separate SSH-passphrase input.
Its software-key setup therefore uses an unencrypted private key. Treat this
as the exportable-key fallback, not as a hardware-backed setup.

```bash
ssh-keygen -t ed25519 -N '' \
  -C 'Example Release Bot <release-bot@example.com>' \
  -f /home/operator/.local/share/release-keys/release-ssh
```

- Set **`RELEASE_SSH_SIGNING_KEY`** to the complete contents of `release-ssh`,
  including its OpenSSH header, footer and newlines.
- Register **`release-ssh.pub`** with the release bot's forge account, or a
  write-capable repository deploy key where the provider supports that route.
- Give the identity only the repository write access required to push the
  prepared release commit and create final release tags. Configure branch/tag
  protection to permit those operations; the private-key secret alone grants
  no forge access. It needs no host administrator privileges.
- Set the workflow's `bot-name` and `bot-email` consistently. For the Forgejo
  layer, also configure the independently verified
  `release-git-host-key-fingerprint`, and `release-git-url` only where SSH
  runs on another host or port than the web interface.

Keep the human who requests a release separate from the bot that publishes it.
Commit authorized human public keys to the request-signature allowlist used by
your layer. Forgejo uses `release-request-allowed-signers` when
`verify-release-request-signature: 'true'`. The bot's public key also needs to
be available to verifiers of its final commits/tags; see
[Git signing](verification.md#signing-the-release-commit--tag-git-objects).

## 3. Create or recover the Cosign key pair

**If a policy repository already contains the trusted `cosign.pub`, use the
matching existing private key.** Creating another pair does not recreate that
identity, even if you use the same filename or passphrase.

For a new signing identity:

```bash
cosign generate-key-pair \
  --output-key-prefix /home/operator/.local/share/release-keys/cosign
```

Enter a strong, non-empty passphrase at the prompts. Do not put it on the command
line or in shell history.

- **`RELEASE_COSIGN_PRIVATE_KEY`**: complete contents of `cosign.key`.
- **`RELEASE_COSIGN_PASSPHRASE`**: the passphrase you entered.
- **`cosign.pub`**: public verification key; store it in the reviewed policy
  repository and supply its exact PEM text through `cosign-public-key`.
  Consumers commonly use a `COSIGN_PUBLIC_KEY` repository/organization variable.

Record the public key's digest:

```bash
sha256sum /home/operator/.local/share/release-keys/cosign.pub
```

Pass the 64-character digest, without the filename, as the literal
`cosign-public-key-sha256` input. Preserve the public key's final newline so
the variable's bytes match that digest. The public key is not a secret.

When recovering a private key, derive its public key and compare it with the
independently trusted policy file:

```bash
cosign public-key \
  --key /home/operator/.local/share/release-keys/cosign.key \
  > /home/operator/.local/share/release-keys/derived-cosign.pub
cmp /path/to/release-policy/keys/cosign.pub \
  /home/operator/.local/share/release-keys/derived-cosign.pub
```

Replace `/path/to/release-policy` with your actual policy checkout. A mismatch
must be investigated, not fixed by blindly replacing the trusted public key.
An intentional rotation updates the public key, its reviewed digest, the
private-key secrets and every consumer's trust configuration together. Retain
old public keys so existing releases remain verifiable.

## 4. Create an OpenPGP package-signing key

This key is needed when the selected release flow signs distro packages with
OpenPGP. In the Forgejo layer, `sign-distro-packages: 'true'` requires the three
GPG secrets; an archive-only consumer can explicitly disable that feature in
both preparation and signing. A Forgejo declaration that says
`sign.method: gpg` requires them too: the same key signs the checksums and
SBOMs.

Use a dedicated keyring, not your personal GPG keyring:

```bash
mkdir -m 700 /home/operator/.local/share/release-keys/gnupg
gpg --homedir /home/operator/.local/share/release-keys/gnupg \
  --quick-generate-key \
  'Example Release Bot <release-bot@example.com>' rsa4096 sign 2y
```

Choose a strong, non-empty passphrase when GnuPG's pinentry prompts you. This
creates a signing-capable RSA key with a two-year expiry; plan renewal before
then. A hardware-backed OpenPGP key needs an appropriate signing integration,
not the software-secret export below.

Display the full primary-key fingerprint:

```bash
gpg --homedir /home/operator/.local/share/release-keys/gnupg \
  --list-secret-keys --fingerprint
```

Export the private key and the public verification key to separate files:

```bash
gpg --homedir /home/operator/.local/share/release-keys/gnupg \
  --armor \
  --output /home/operator/.local/share/release-keys/release-gpg-private.asc \
  --export-secret-keys 'Example Release Bot <release-bot@example.com>'

gpg --homedir /home/operator/.local/share/release-keys/gnupg \
  --armor \
  --output /home/operator/.local/share/release-keys/release-gpg-public.asc \
  --export 'Example Release Bot <release-bot@example.com>'
```

- **`RELEASE_GPG_PRIVATE_KEY`**: complete armored private-key file.
- **`RELEASE_GPG_FINGERPRINT`**: full primary-key fingerprint without spaces,
  not the short key ID and not a subkey ID.
- **`RELEASE_GPG_PASSPHRASE`**: the passphrase chosen during generation.

Publish the public key and its trusted fingerprint through your documented
release-verification channel. Keep the generated revocation certificate from
the dedicated keyring's `openpgp-revocs.d` directory in protected backup.

## 5. Configure secrets and check the setup

| Secret | Value to enter in the forge's secret store |
| --- | --- |
| `RELEASE_SSH_SIGNING_KEY` | Entire `release-ssh` private-key file |
| `RELEASE_COSIGN_PRIVATE_KEY` | Entire encrypted `cosign.key` file |
| `RELEASE_COSIGN_PASSPHRASE` | Cosign passphrase |
| `RELEASE_GPG_PRIVATE_KEY` | Entire `release-gpg-private.asc` file |
| `RELEASE_GPG_FINGERPRINT` | Full primary-key fingerprint, without spaces |
| `RELEASE_GPG_PASSPHRASE` | GPG passphrase |

Configure these through the forge's protected repository or organization secret
settings, restricting organization secrets to the intended release repositories.
Pass them only to the signing/preparation jobs that need them, not to untrusted
pull requests or ordinary build jobs. Preserve multiline key values exactly.

Before the first release, confirm:

1. The bot's public SSH key and branch/tag permissions match the configured
   private key, and the SSH host fingerprint comes from a trusted source.
2. The Cosign private key derives the trusted public key, and that public key
   hashes to the consumer's literal digest.
3. The GPG private key's full primary fingerprint matches the configured value;
   its public key is available to package verifiers.
4. API and registry credentials are configured separately with the rights the
   chosen [delivery layer](reference.md) requires. Signing keys are not API
   tokens, and publishing a signer image does not require copying release keys
   into that image.
5. The signing runner meets the layer's isolation and
   [swap policy](verification.md#swap-policy).

Keep encrypted backups of keys, passphrases and recovery material. Do not
commit private files, print them in CI logs, or paste them into chat or issues.
A passphrase protects a stored file, not a key whose passphrase sits in the
same CI secret store: a compromised signing job can use both.
