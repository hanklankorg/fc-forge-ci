# Tutorial: your first pull-request check

This walks a Go project on GitHub from a bare repository to a pull request that
Forge CI checks, in about fifteen minutes. The same two commands, `init` and
`doctor`, do the setup for Cargo, Maven, Gradle, npm and Android projects;
only the example and the files differ. GitLab and Forgejo have their own
layouts; follow this page once, then read the [GitLab](gitlab.md#getting-started)
or [Forgejo](forgejo.md#getting-started) guide for the differences.

No secrets are needed for pull-request checks. Releases need them, and the last
section says where that setup lives.

## Before you start

You need:

- the `forge-ci` binary on your `PATH` (see [Install](../README.md#install));
- a Git repository with one root manifest, here `go.mod`, pushed to GitHub,
  with a Git identity configured;
- network access to `github.com`: `init` reads the examples from the Forge CI
  release it pins.

Run every command from the repository root.

## Step 1: preview what init would write

```bash
forge-ci init https://github.com/diggsweden/forge-ci --dry-run
```

The URL is the Forge CI repository to copy the examples from, not your
project's remote. The preview reads your build files, picks an example, pins
the newest release, and reports what it would do:

```text
Forge CI initialization preview

Template source: https://github.com/diggsweden/forge-ci
Release: v0.93.2
Forge: github
Example: go-cli

Would create:
  .forge-ci/artifacts.yml
  .github/workflows/pullrequest-workflow.yml
  .github/workflows/release-workflow.yml

From your build files:
  project name   hello
  main package   .

Still to fill in before running
  .github/workflows/pullrequest-workflow.yml:36  PIN_DIGEST
  .github/workflows/release-workflow.yml:48  FIXME
  .github/workflows/release-workflow.yml:49  FIXME-release-bot

How to fill them
  ...
forge-ci doctor fails until they are filled; values only the release workflow reads warn until then.

No project files were written. Templates were fetched into temporary storage.
```

Three things to check before going on:

- **Forge** says `github`, because the URL is on `github.com`. On another host,
  the forge comes from the mirror's `.forge-ci/consumer-defaults.yml`.
- **Example** matches your project. `go-cli` is a binary shipped as release
  files; a service shipped as a container is `go-service`. Pass
  `--example go-service` to choose it.
- **Would create** lists no file you already have. Existing files are never
  overwritten; a conflict is reported and the command returns nonzero.

## Step 2: write the files

```bash
forge-ci init https://github.com/diggsweden/forge-ci
```

The report is the same as the preview, with `Created:` in place of
`Would create:`, and it ends with a `Next:` line. The project name and main
package came from `go.mod` and your `main` package; check them under
`From your build files`.

A pull request needs one value: `PIN_DIGEST`, the digest that pins the lint
image on line 36 of `.github/workflows/pullrequest-workflow.yml`. `How to fill
them` gives the command that reads it. Replace `PIN_DIGEST` with the digest,
keeping `@sha256:`. The two `FIXME` values belong to the release workflow and
can wait until your first release.

## Step 3: check the result

```bash
forge-ci doctor
```

```text
Environment:
  enforcement:          blocks nothing; reports without blocking lint, vulnerabilities, application-tests, reproducible-builds. ...

[OK] artifacts.yml present: .../.forge-ci/artifacts.yml
[OK] artifacts.yml parses + validates: 1 artifact(s), 0 container(s), sign.method=sigstore
[OK] administrator policy resolves: no administrator setting in the environment
[OK] CI workflows present: 2 CI file(s): ...
[WARN] example values filled in: 2 example value(s) still to fill in: ... (release workflow only: the pull request runs without them)
[OK] workflows pin forge-ci to a tag: no @main / @master references found
[OK] sign block valid: method=sigstore
[OK] artifact paths exist: 1 artifact(s); every working directory and Go main package exists
[OK] workflow id-token permission: a workflow grants the OIDC id-token ...
[WARN] ssh git-signing allowlist: git-signing.method=ssh, but .forge-ci/allowed_signers is missing ...
       fix: add the release bot's key and the key of each person who requests releases: `forge-ci setup signer ...`

Scope: local configuration checks only; remote secrets, runner availability and publishing permissions were not checked.
Next: review the WARN lines above, then commit the Forge CI files and open a pull request; ...
```

`OK` and `WARN` exit 0; a `FAIL` exits 1 and comes with a `fix:` line. Both
warnings above are about releases, which this tutorial does not reach: the
release workflow's bot values, and the allowlist the release checks the
person who requests a release against. Pull-request checks need neither.

The `enforcement` line matters before you merge: by default a failing test,
lint finding or known vulnerability is reported on the pull request without
failing it. `forge-ci docs admin-policy` says how an administrator makes a
check block.

`doctor` reads only the local repository. It cannot tell whether a secret is
set on GitHub or whether a runner is available; the pull request does that.

## Step 4: open the pull request

```bash
git checkout -b forge-ci
git add .forge-ci .github
git commit -s -m "ci: add Forge CI pull-request and release workflows"
git push -u origin forge-ci
```

Open the pull request on GitHub. The `Pull Request Workflow` runs the
`pr-checks` job from the pinned Forge CI release: lint through Nanolinter and
security scans, with a step summary of what ran. It does not run your own
tests; add your project's test job to the same workflow and require both
before merging. The first run pulls the runtime images, so it takes longer
than later ones.

A failing lint check names the tool and the finding in the job log. Fix it and
push; the workflow runs again.

Once the check is green, merge. Every later pull request against `main` or
`develop` gets the same check.

## Where to go next

- **A release** is the second workflow you just committed. It starts when you
  push a signed `release-request/vX.Y.Z` tag, and it needs secrets and a
  signing allowlist first: [set up release signing secrets](quickstart-setup-secrets.md),
  then [Secrets and tokens](github.md#secrets-and-tokens),
  [protect the release tags](github.md#protect-the-release-tags) and
  [Release authorisation](verification.md#release-authorisation).
- **A container instead of binaries:** the [Go service example](../examples/go-service/)
  and [Ecosystems](ecosystems.md) explain artifact-first against
  container-first.
- **Custom lint policy:** `init` writes no linter configuration. Copy the
  example's `nanolinter.toml` when you want to change the defaults; see
  [Pull-request gates and lint](github.md#pull-request-gates-and-lint).
- **Everything the CLI can do:** the [CLI reference](cli-reference.md). Most
  of it is run by the workflows, not by you.
