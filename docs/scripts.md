# Scripts

The Go CLI owns the core workflow operations. Scripts and third-party actions
still handle bootstrap, environment setup and some artifact handoffs. The
`scripts/` entry points are:

| Path | Purpose |
|------|---------|
| `scripts/bootstrap/install-*.sh` | Toolchain installers invoked at runtime-image build time, plus `install-forge-ci.sh` for plain/macOS runners that cannot use the runtime container. Renovate-managed pinning lives here where applicable. |
| `scripts/ci/run-live-tests.sh` | The live conformance tier's entry, behind `just test-live` and `just test-live-consumer`. |
| `scripts/ci/publish-live-signer-image.sh` | Builds and publishes a signer image to the configured Forge Lab registry for authorized live-tier runs; see [live testing](testing.md). |
| `scripts/ci/forgejo-binary.sh` | The shared delivery-binary build, checksum and publisher: behind `just refresh-forgejo-binary`, the recorded-build guard, and Forgejo/GitLab generic package publication. See [GitLab bootstrap](gitlab.md#checksum-bound-engine-bootstrap) for GitLab coordinates and token types. |

The Forgejo layer has shell of its own under `.forgejo/`: the resolver
`.forgejo/bin/lib-forge-ci.sh`, `.forgejo/scripts/verify-signer-toolchain.sh`
and two action scripts. They run in consumer jobs from a checkout of this
repository and are part of that layer's contract; see the
[Forgejo guide](forgejo.md).

For the **CLI surface** available to workflows, see
[docs/cli-reference.md](cli-reference.md). That file is auto-generated
from the binary's urfave/cli command tree by `cmd/gen-cli-reference`;
run `just gen-cli-reference` after any CLI change.

## Why a `forge-ci` binary

1. **One Go command surface for core operations.** Workflows call
   `forge-ci <group> <subcmd>` for those operations. The binary is baked into the
   runtime container. On plain runners, tagged releases install a
   cosign- and SHA-256-verified release archive; branch/SHA refs, local refs,
   and explicit `FORGE_CI_USE_GO_INSTALL=1` overrides select source installation,
   subject to the [current module restriction](DEVELOPMENT.md#source-checkout).
2. **Typed values and errors.** The domain/app/adapter split keeps business
   rules and external operations behind one CLI binding per use case.
3. **Cross-provider shape.** The use cases are written behind provider-specific
   adapters. GitHub Actions workflows and GitLab CI Catalog components use the
   same binary-owned contracts; capability status is documented in
   [providers.md](providers.md).
