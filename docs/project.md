# Forge CI project reference

Forge CI is a forge-neutral build and release engine with GitHub, GitLab and
Forgejo delivery layers. Its first planned release is **0.90.0**.

## Project identity

| Surface | Name |
|---|---|
| Engine repository and executable | `forge-ci` |
| Engine Go module | `github.com/diggsweden/forge-ci` |
| Companion repository | `forge-ci-blackbox-tests` |
| Companion Go module | `github.com/diggsweden/forge-ci-blackbox-tests` |
| Configuration and build metadata directory | `.forge-ci` |
| Product environment prefix | `FORGE_CI_*` |
| Companion binary override | `FORGE_CI_TEST_BINARY` |
| Workflow input prefix | `forge-ci-*` |
| Installer | `install-forge-ci.sh` |
| Layer setup action | `setup-forge-ci` |
| Layer binary checksum | `.forgejo/bin/forge-ci.sha256` |
| Package and image prefix | `forge-ci` |
| Development branch | `main` |
| Release baseline | `v0.90.0` |
| Rolling prerelease channel | `v0.90.0-pre` |

The Go module has no major-version suffix. Third-party module paths and API
versions follow their own contracts. Provider names, the `blackbox` build tag,
and generic `CI_*`, `RC_*` and `BLACKBOX_*` controls retain their meanings.

Project-owned headers and REUSE metadata use
`2026 Digg - Agency for Digital Government`. Third-party licence notices retain
their original attribution.

## Companion wiring

The suite builds `../forge-ci/cmd/forge-ci` by default. `FORGE_CI_TEST_BINARY` selects an
explicit binary instead. A binary-identity test checks that its help names
Forge CI. Strict mode requires complete test selection and the egress audit.

Both engine self-workflows pin the companion by a reviewed commit. The
companion's drift workflow defaults to `diggsweden/forge-ci` on `main` and
records the resolved engine commit for each run.

## Before 0.90.0 publication

- Complete the engine race, integration and smoke gates and the companion's
  complete strict black-box gate against the intended release binary.
- Regenerate the CLI reference, schemas and engine checksum from that revision.
- Publish the engine source, checksum-bound package, runtime images and signer
  image at matching coordinates before enabling remote workflows.
- Authenticate each selected revision independently before setting approval
  variables. A pin selects code; an approval states trust in that code.
- Exercise the selected forge's bootstrap, signing and publication paths in the
  disposable lab and retain evidence for that exact revision and binary.

See [testing](testing.md), [runtime images](runtime-images.md),
[verification](verification.md), and [open questions](open-questions.md) for
current requirements. This documentation does not imply that the prospective
0.90.0 tags or image coordinates have already been published.
