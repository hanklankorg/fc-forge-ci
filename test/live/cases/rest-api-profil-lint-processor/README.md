# REST npm application operator case

Source: `/home/jos/development/github.com/diggsweden/forge-ci-migrations/rest-api-profil-lint-processor`.
Engine: `/home/jos/development/github.com/diggsweden/r3/forge-ci`.

The source is admitted as a clean Git revision. The fixture reads regular tracked
`package.json` and `Containerfile` blobs through the existing exact-path/mode
reader and stages only CI policy in Consumer's private index. Original npm
scripts, engine bounds, application files, resources and Containerfile remain
source-owned. The shared `forge-ci build npm run` performs npm ci, tests, build,
SBOM and pack. No host npm or application execution is needed for preparation.

## Operator execution

Load the usual **Compose Forgejo** lab-contract environment and then source
`test/live/cases/rest-api-profil-lint-processor/operator.env`. From the engine
checkout:

```sh
bash scripts/ci/run-live-tests.sh focused "$LIVEFILTER"
```

This selects `TestForgejoLayer_ConsumerTrial/public`, public disposable
repositories, native amd64, Go 1.27.3 and stable release **2.1.1**. The normal
release preparer bumps manifests only in the scratch repository. Existing SDK contract acceptance and scratch
ownership checks apply. Existing `logJobs` retains bounded redacted traces for
successful and failed jobs before scratch cleanup.

The separate development case keeps the original Containerfile snapshot **first**. Known upstream Forgejo
issue #12191 can block it. Do not change to artifact-first to conceal that
failure. The later package check is not evidence that an image service started;
this case currently makes no service-readiness claim.

To select that development case explicitly (currently blocked by the upstream
image-first issue; it is not part of stable files acceptance):

```sh
export LIVEFILTER='^TestForgejoLayer_NPMApplicationDevelopment$'
bash scripts/ci/run-live-tests.sh focused "$LIVEFILTER"
```

The stable wrapper reuses `hello-npm-service` preparation, artifact handoff and
sign-and-publish recipes, with npm version bump to **2.1.1**, a pinned Node bump
image and container publication disabled for this package-only variation. The
retained `.tgz` is uploaded before the oracle. The oracle installs that tarball
into a fresh prefix/cache, runs `raplp --version`, `raplp --help` and a small
OpenAPI document, and requires a diagnostic report. Missing packed resources
are an **APP_FINDING**, never a reason to add application resources to the
overlay. Signing and file publication complete before the separate oracle and
registry jobs start; either later finding leaves signed files available while
the overall trial correctly reports failure.

The publisher has no project checkout. It downloads the retained producer
tarball, checks SHA-256 and exact name/version against producer metadata, and
uses the existing safe `publish npm validate-tarball` command in a disposable
verification directory. It never repacks or executes lifecycle scripts. The
existing typed npm deploy command uses `--ignore-scripts`, temporary credential
configuration, tag binding and packed metadata checks. Registry configuration
and existence checks use `publish npm write-npmrc` and `validate-version`.
The Forgejo tag policy is `release.{version}` (for example `release.2.1.1`),
using the product's existing immutable version-bound alias. Other forge/signing credential
environment variables are stripped before registry commands.

The SDK trial binds `NPM_APPLICATION_REGISTRY` to its accepted Forgejo target's
`/api/packages/<owner>/npm/` endpoint. This supports scoped and unscoped source
package names without renaming manifests. The registry token is scoped to the
publisher step. A conflicting packed publishConfig is rejected by the product.

## Verification and limits

Preparation checks passed with explicit Go 1.27.3 PATH/GOROOT:

- `TestNPMApplicationFixtureSourceBoundary`: SHA-1/SHA-256 object and executable
  mode preservation, clean original checkout, rejected package metadata symlink.
- `TestNPMApplicationActualSourceRenderer`: actual source revision
  `1011280d6b50f29ddd3dda936bc9c769d58b460f`; stable and development diffs
  restricted to `.forgejo/`, `.forge-ci/` and stable changelog policy files.
- Live conformance package compilation (`go test -tags live ... -run '^$'`).
- Publisher YAML/command shape, absence of checkout/lifecycle/signing credentials,
  explicit child-credential stripping, and existing typed npm publication,
  temporary credential cleanup, safe tarball and version-check unit tests.
- `bash -n` for the oracle/environment and `git diff --check`.

Read-only `skopeo inspect` on 2026-10-29 resolved
`docker.io/library/node:24.17.0-trixie` to
`sha256:61db8992b5c481488fe236ea69fe94035ba73df76a474051ed2e9713f3a15e5a`;
image configuration reported linux/amd64 and `NODE_VERSION=24.17.0`, satisfying
the source's `<=24.17.0` bound. Runtime execution remains an operator CI check.

`5c48…` identifies the operator's binary SHA-256/SRI, not a Git revision.
The current engine's Maven-only `publish forge-packages --from-build` is
expected and is not used for npm. The separate native `publish npm` API handles
the retained package. The existing SDK layer mechanism supplies/verifies the
binary; no guessed binary digest or alternate CLI revision is introduced.

### Run 278: owning failure and correction

Run 278's producer passed 22 suites / 312 tests. Signing and file publication
jobs succeeded, but registry publication failed HTTP 400 and the installed
oracle was cancelled. These statuses are not complete stable acceptance or an
independent host verification of signed files.

The fixture selected `release-{version}`, which the product correctly resolved
from packed metadata to `release-2.1.1`. The owning integration error was that
selection for Forgejo. Forgejo 16.0.6's `setPackageTag` rejects any tag accepted
by `version.NewVersion`. Its `go.mod` replaces `github.com/hashicorp/go-version`
with `github.com/6543/go-version v1.3.1`; that parser permits optional word/hyphen
prefixes (`(?:\w+\-)*`), so `release-2.1.1` is recognized as a version. A dotted
`release.2.1.1` does not match that prefix grammar. The fixture now selects the
already-supported dotted alias for both preflight and deploy via one NPM_TAG.
No channel-tag relaxation or implicit tag rewriting is introduced.

Upstream sources inspected read-only:

- https://codeberg.org/forgejo/forgejo/src/tag/v16.0.6/routers/api/packages/npm/npm.go
- https://codeberg.org/forgejo/forgejo/src/tag/v16.0.6/go.mod
- https://github.com/6543/go-version/blob/v1.3.1/version.go

`UploadPackage` calls `CreatePackageAndAddFile` before looping over DistTags
with `setPackageTag`. The latter's transaction covers tag updates, not the
already-created package version/file. Therefore its HTTP 400 is compatible
with an existing untagged version, as observed for package 1230. A retry must
not interpret absence of a tag as absence of a version. The operator already
removed the exact partial version and verified absence.

The fixture's local preflight now resolves the compatible dotted alias before
the publish mutation. Existing product preflight checks policy, packed version,
publishConfig, and existing tag bindings, but does not ask an arbitrary server
to dry-run its tag grammar; `validate-version` checks version existence only.
Thus local validation cannot promise server-wide atomicity or prevent unrelated
post-mutation failures. The focused regression preserves canonical version
binding (including prerelease/build metadata behavior), shared preflight/deploy
selection, and refusal of movable channels. No live retry was launched.

`hello-npm-service` includes native Forgejo and GitLab wrappers;
`hello-npm-package` supplies GitHub packed-install coverage. This new adapter
is wired to Forgejo only. GitLab and GitHub actual-source acceptance remain
unimplemented/unrun; GitHub personal-versus-organization acknowledgement stays
operator policy. No live SDK acceptance, registry package publication or service
container execution has been performed during fixture preparation.
