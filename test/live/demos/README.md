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
  job.Containerfile    optional: its GitLab job image, built by `just demo-image`
  build.Containerfile  optional: its GitLab release build image, when the job
                       image cannot build the release (Buildah for an image)
```

The consumer names Forge CI as a published project does; the live tier puts
the layer under test in its place, as it does for any consumer. Its Forgejo
workflows sit in `.forgejo/workflows`, its GitLab merge-request gate in
`.gitlab/merge-request.yml` with the scripts the release runs, and its GitHub
workflows in `test/live/github/workflows`. `hello-maven` is a Maven jar;
`hello-maven-service` adds the runtime image built from that jar;
`hello-npm-service` is an npm package and the image built from it.
`hello-cargo-container` is a Rust service whose Containerfile is the build,
its binary extracted from the image. `hello-npm-package` adds an installable,
scoped npm package without a container. `hello-cargo-workspace` adds two
workspace binaries, each with its own runtime image and binary export target.
A demo for another ecosystem or feature is one more directory in the same shape;
`internal/livetest/demos_internal_test.go` renders every consumer here for
each forge it declares and parses its artifact configuration, so malformed
inputs fail `just test-unit`. Rendering is not a live-flow pass.

## Coverage still to execute

| Fixture | Declared forge workflows | Distinct coverage |
|---|---|---|
| hello-maven | GitHub, Forgejo, GitLab | Maven JAR |
| hello-maven-service | GitHub, Forgejo, GitLab | Executable JAR and runtime image |
| hello-npm-service | GitHub, Forgejo, GitLab | Private npm application archive and image |
| hello-cargo-container | GitHub | Container-first Rust binary |
| hello-npm-package | GitHub | Publishable scoped package; offline packed-package installation test |
| hello-cargo-workspace | GitHub | Shared version; two binaries and images |
| hello-android | GitHub PR/build | Demo flavor, unit tests, unsigned release APK/AAB |
| hello-ios | GitHub PR/build | macOS ARM64 simulator tests, XcodeGen app, unsigned archive |

Record live results separately by commit, provider, visibility and flow. Local
package, binary and Android build tests do not establish registry permissions,
release signing or promotion. The iOS build requires macOS. The mobile
fixtures currently provide build workflows, not signed release workflows.
Forgejo/GitLab wiring for the new fixtures, web archive coverage and
development-release parity remain outstanding. Run public
amd64 first, then private PR/MR and release flows; mixed architectures are deferred.

PR build/test jobs are separate from the reusable PR lint orchestrator. A green
lint job alone does not prove the project built or ran its tests. Signing-capable
mobile build leaves remain branch-only; PRs receive no signing secrets. macOS
ARM64 iOS tests are a supported independent track, not deferred Linux multi-arch
coverage. Forgejo hello releases verify a signed request from a disposable
requester, using the trial harness's public allowed-signers entry.

## Java runtime scan regression

The Java service uses a digest-pinned distroless Java 25 non-root runtime.
The previous Temurin base included `/usr/bin/pebble` built with Go 1.27.1,
which caused three fixable HIGH findings. Updating Trivy alone did not remove
them. The distroless image removes that unneeded helper and retains the same
executable JAR contract. Check the application response as well as the scan
when changing the base. A passing gate may still report unfixed HIGH findings;
do not describe that result as vulnerability-free.

docs/testing.md, Showing a consumer, has the commands, the images each forge
needs, and how to delete a demo.
