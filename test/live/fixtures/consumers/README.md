<!--
SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government

SPDX-License-Identifier: CC0-1.0
-->

# Demos

Each directory here is one demo `just demo <forgejo|gitlab|github> <name>`
shows: a small consumer that uses Forge CI the way a project would, released
and then reviewed in a pull or merge request, and kept for people to look at.

```text
<name>/
  consumer/            the project, committed as it is into the demo repository
```

A consumer pins Forge CI as a published project does; the live tests put the
Forge CI under test in its place. Its Forgejo workflows are in
`.forgejo/workflows`, its GitLab merge-request gate in
`.gitlab/merge-request.yml` with the scripts its release runs, and its GitHub
workflows in `test/live/github/workflows`.

On GitLab every demo runs in Forge CI's own images, built from this tree. Its
checks and release jobs run in the runtime image of its toolchain (`just
runtime-image-archive java|node|rust|go|android <directory>`) as the image's
`nonroot` user (`job-user: "65532"` in the gate, `job-user=65532:65532` for
the release). Its release images build in the Buildah job image (`just
buildah-job-image-archive <directory>`). No demo carries an image of its own.
A gate that builds with a component runs the engine of its job image, so the
Go and Gradle demos take the candidate's runtime image with `just demo gitlab
<name> job-archive=<archive>`. `hello-monorepo` checks its backend in the Java
image (`job-archive=`) and its frontend in the Node image (`job-archive-node=`).

`hello-base-images`, `hello-go-cli`, `hello-go-service`, `hello-gradle`,
`hello-meta` and `hello-monorepo` are copies of an example: their source is
their own, and their CI files are the example's, filled in by
`test/live/internal/livetest/exampledemos_internal_test.go`, which fails when
one drifts. The GitLab release scripts of `hello-go-cli`, `hello-gradle` and
`hello-meta` are their own, since their examples ship none. After changing an example, rewrite them with
`go test ./test/live/internal/livetest -run TestExampleDemosAreTheirExamples -args -update-demos`.
`test/live/internal/livetest/demos_internal_test.go` renders every demo for
each forge it declares and parses its artifact configuration, so a malformed
demo fails `just test-unit`. Rendering is not a live run.

## Flows each demo declares

Each cell is the demo's workflows for that forge: the pull or merge-request
gate, then the release. A dash is a flow the demo does not declare; the
reasons follow the table.

| Fixture | GitHub | Forgejo | GitLab | Distinct coverage |
|---|---|---|---|---|
| hello-maven | PR, release | PR, release | MR, release | Maven JAR |
| hello-maven-service | PR, release | PR, release | MR, release | Executable JAR and runtime image |
| hello-gradle-service | PR, release | PR, release | MR, release | Executable JAR built by Gradle and its runtime image; an artifact-first snapshot of a Gradle producer |
| hello-npm-service | PR, release | PR, release | MR, release | One release of signed files, the package in the forge's registry and the image |
| hello-cargo-container | PR, release | PR, release | MR, release | Container-first Rust binary |
| hello-cargo-workspace | PR, release | PR, release | MR, release | Shared version; two binaries and images |
| hello-npm-package | PR, release | PR, release | MR, release | Publishable scoped package; offline packed-package installation test; an npm workspace released as its root package, the member bundled in the tarball |
| hello-maven-reactor | PR, release | PR, release | MR, release | Maven reactor: one build, one version bump and one aggregate Build SBOM for a parent POM and two modules; an executable application jar without an image, run in the gate |
| hello-cargo-cli | PR, release | PR, release | MR, release | Cargo without a container: an artifact-first binary with its Build SBOM; a library crate in the workspace, linked and not released |
| hello-android | PR/build | - | MR | Demo flavor, unit tests, unsigned release APK/AAB |
| hello-android-library | PR/build, release | - | MR | Library module: unit tests, Android Lint, release AAR; the release publishes its Maven publication to the package registry |
| hello-ios | PR/build | - | - | macOS ARM64 simulator tests, XcodeGen app, unsigned archive |
| hello-go-cli | PR, release | PR, release | MR, release | The go-cli example as shipped; `cmd/my-cli` layout. `test/live/forgejo-packages` holds the example's GoReleaser release with distro packages, which a Forgejo release trial stages over the root with `fixtures=` |
| hello-go-service | PR, release | PR, release | MR | The go-service example as shipped; binaries and runtime image |
| hello-gradle | PR, release | PR, release | MR, release | The gradle-app example as shipped; reproducible Java library jars |
| hello-monorepo | PR, release | PR, release | MR, release | The monorepo example as shipped; Maven and npm services checked together and released as one version: both files, the package and two images |
| hello-base-images | - | release | MR, release | The base-images example as shipped; a base image built from nothing and two flavors built on its digest, released with a signed inventory of all three |
| hello-meta | PR, release | PR, release | MR, release | The no-build example as shipped; scripts and configuration only: no build, no version bump. On GitHub the final tag is on the requested commit and the release offers no file; on Forgejo and GitLab the release commit changes the changelog alone and the release offers the source archive |

Why a flow is missing:

- **iOS on Forgejo and GitLab**, deliberately: the build needs macOS ARM64
  runners with Xcode, and neither lab has one. GitLab's components build no
  Xcode either.
- **Mobile app releases**: neither app demo declares a release on any forge.
  The build jobs that can sign run on branches only, and the lab has no
  throwaway signing setup for them.
- **The Android library's release on GitLab and Forgejo**: the generated
  GitLab publish stage cannot hand the exported publication over, and
  Forgejo has no Android build.
- **Android on Forgejo**: the Forgejo lane's job image is a nanolinter
  flavour, which has no Android SDK, and the lane publishes no runtime image
  to run the build in.
- **The example demos' GitLab releases**: the GitLab examples are the
  merge-request gate; the release policy's commands are the adopter's, and no
  example but the monorepo's ships them. `hello-gradle`, `hello-go-cli` and
  `hello-meta` carry their own, `.gitlab/check-release.sh` and
  `.gitlab/build-release.sh`, which run in the plan's build job of their
  family and the release files job; `hello-meta` has no family, so its build
  runs in the release files job alone. `hello-go-service` has none.
- **`hello-base-images` pull requests and GitHub**: its example is a release
  of base images with a GitLab gate; it ships no Forgejo pull-request
  workflow and no GitHub workflows.
- **A release without a file on Forgejo and GitLab**: `hello-meta` releases
  the tag and its notes alone on GitHub. The signer of the other two layers
  takes a hand-off of at least one file, so there the demo hands over the
  source archive of the release commit.

A GitLab release renders the changelog the way the demo's files call for:
with the component's default, git-cliff, when the demo carries the
`.forge-ci/gitcliff/` configuration `forge-ci init` writes (as
`hello-cargo-workspace` does), and otherwise with git-chglog from `.chglog/`.
The Forgejo releases name their renderer in their workflow.

The npm releases on Forgejo and GitLab ship the tarball as a signed release
file. `hello-npm-service` also publishes it to the owner's package registry
on Forgejo, with a job of its own workflow, since the layer has none;
`hello-npm-package` does not. On GitLab the release policy publishes the
package of both to the project's registry, as their `artifacts.yml` declares.

A demo's development build is a snapshot: the development phase of its
`lifecycle` run, a service snapshot of its one container read from the
declaration its release builds its images from, or a snapshot run of its
own. Matrix 1 of [Testing matrices](../../../../docs/testing-matrices.md) shows which one each forge
runs, and [Lifecycle](../../../../docs/live-runs.md#lifecycle) says why a demo has none.

The pull-request build and test jobs are separate from the lint gate, so a
green lint job does not show that the project built or ran its tests. Pull
requests get no signing secrets. A Forgejo release checks a signed release
request from a throwaway requester against the test harness's public
allowed-signers entry.

docs/live-runs.md, Showing a consumer, has the commands, the images each forge
needs, and how to delete a demo.
