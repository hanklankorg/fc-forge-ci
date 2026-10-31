# Public npm release package fixtures

The public Forgejo consumer release trial reuses the native npm application's
preparation, build, artifact handoff and signing recipe. It retains the built
tarball and metadata, publishes those bytes in a separate job, and runs an
operator-supplied fresh-install oracle in another job. The publisher has no
source checkout, project lifecycle execution or signing keys.

Supply the existing release trial inputs plus:

- `FORGE_CI_LIVE_NPM_APPLICATION_IMAGE`: reviewed digest-pinned Node job image.
- `FORGE_CI_LIVE_NPM_APPLICATION_ORACLE`: absolute regular shell script, called
  with the retained-package directory. Verify its checksums, install the local
  tarball into a fresh consumer with scripts disabled, and check its public API.
- `FORGE_CI_LIVE_NPM_LIBRARY=true`: explicit stable-library mode, accepting a
  publishable package with a literal `main` or string `exports` entry instead of
  requiring a CLI `bin`.
  The default remains CLI/application mode. Private packages are refused.

Run `just test-live '^TestForgejoLayer_ConsumerTrial$/public$'` with the usual v5
contract, public visibility, clean consumer, release event, version and reviewed
signer image. Library mode has no image-development fixture. This mode changes
test infrastructure, not administrator enforcement policy.

The demo's `@example-org` scope is a parameter: choose the admitted disposable
registry owner before committing the clean input, consistently in package.json
and the lockfile. Keep the source implementation/tests intact and record the
chosen scope. Registry publication and a fresh retained-tarball install are
separate receipts; do not describe the latter as an install from the registry.

The fixture creates its own `dist/` handoff directory. A library's original build
may only check source syntax and legitimately produce no application `dist/`.
The real original build/tests still run before signing and publication.

The SDK validates packed library metadata and the declared regular entrypoint
inside private extraction staging, without executing package code. CLI packages
retain the historical `dist/cli.js` requirement; library metadata cannot mask a
missing entrypoint for a package that declares a CLI `bin`.
