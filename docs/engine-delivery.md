# Engine delivery on GitHub

How the GitHub workflows build the forge-ci binary for a run, hand it to the
jobs that need it, and choose the tools those jobs may call. This page is for
maintainers. Jobs that run in a runtime-image container and do not receive
the delivered binary, such as the `build-*.yml` product builds, use the
binary baked into that image instead; see
[CLI binary channels](runtime-images.md#cli-binary-channels).

## Build and hand-off

`canonical-cli-bootstrap.yml` is a reusable workflow with read-only
permissions and no signing or publishing credential. Each orchestrator calls
it once with four pinned inputs: the engine repository, the full engine
commit (`forge-ci-binary-ref`), a Go builder image and a receiver image, both
by `@sha256:` digest. It has three jobs:

1. `canonical-admit` checks the input formats before any image starts.
2. `canonical-build` checks out only the engine commit, without credentials,
   LFS or submodules. `scripts/ci/canonical-cli-delivery.sh build` downloads
   the modules into a fresh private cache with `go mod download`, using the
   Go release `go.mod` pins and checksum-database verification, and stops if
   that changes `go.mod` or `go.sum`. It then builds and verifies the
   linux/amd64 and linux/arm64 binaries with `scripts/ci/engine-binary.sh`
   against the checkout's `.engine/bin/*.sha256` records, and uploads the two
   files as `forge-ci-canonical-linux-<run>-<attempt>-<commit>`, with
   overwrite off.
3. `canonical-receive` checks out the engine again and receives the
   artifact the way every later job does.

Every Linux job of a route that runs on the plain runner receives the
binary through the `consume-canonical-cli` action, before it checks out
anything else. So do the orchestrators' planning jobs that run inside the
runtime image (`compose-pr-plan`, `parse-config`,
`summarize-prepare-stage`, `setup-snapshot`). The job needs the
`actions: read` permission to read the artifact's metadata and download it:

- It checks that the producer's run, attempt and engine commit match its
  own run.
- It reads the artifact's metadata and checks its ID, name, run, repository,
  source commit, expiry and size (at most 128 MiB).
- It downloads exactly that artifact ID, from this run, into an empty
  directory.
- The directory must hold exactly `forge-ci-linux-amd64` and
  `forge-ci-linux-arm64`, both regular, nonempty files of at most 128 MiB,
  and both must match the engine's `.sha256` records.
- The job makes the file for its own architecture executable and runs it
  through the bootstrap below, first with `--version`.

The binary is only valid in the run and attempt that built it. To retry,
rerun the whole chain, not one receiving job.

## Bootstrap

`scripts/ci/trusted-bootstrap.sh` runs the delivered binary. It takes the
engine checkout, its HTTPS origin, the full commit, the absolute path of the
binary and a tool profile, then `--` and the CLI arguments. Before it runs
anything from the checkout it:

- checks that the checkout's `HEAD` and origin match the commit and origin
  it was given;
- compares `.engine/bin/lib-forge-ci.sh`, the two `.sha256` records and
  `scripts/ci/trusted-runtime-tools.sh` byte for byte with the commit;
- clears `FORGE_CI_BIN`, `FORGE_CI_PATH`, `FORGE_CI_BINARY_REF` and
  `FORGE_CI_PLAN`, and verifies the binary's SHA-256 strictly against the
  record.

Only then does it run the binary, through `trusted-runtime-tools.sh`. It
never looks for `forge-ci` on `PATH`, so an older CLI in an image is never
picked.

The helper relies on its caller for the rest: a fresh engine checkout made
by the trusted checkout action, a bash started without a startup
environment the consumer controls (such as `BASH_ENV`), and no other
process writing to the checkout or the binary while it runs. It does not
check that the whole checkout is clean, only the files listed above.

## Tool profiles

`scripts/ci/trusted-runtime-tools.sh` sets `PATH` for the CLI from one of
three fixed profiles. The workflow states the profile as a literal
(`tool-profile` on the action), and an unknown name is refused.

| Profile | Used by | Environment |
|---|---|---|
| `runtime` | jobs that run in the approved runtime image | `PATH=/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/bin:/bin`, `RUSTUP_HOME=/usr/local/rustup`, `CARGO_HOME=$HOME/.cargo` |
| `host` | hosted-runner jobs that hold signing keys, a registry or release token, or the consumer checkout next to Docker or Buildah | `PATH=/usr/bin:/bin` |
| `host-toolchain` | only `prepare-version-files`, which runs `mvn`, `npm` or `cargo` and holds no key | `PATH=/usr/bin:/bin:/usr/local/bin:$HOME/.cargo/bin`; `HOME` must be one absolute directory |

A profile sets only these variables; the rest of the job environment, such
as `JAVA_HOME`, reaches the CLI unchanged. No profile reads a caller's
`PATH`, the workspace or an install directory.

The `host-toolchain` tools are not verified: on the hosted image some of
their directories are writable by the runner user. The job that uses them
holds no key, and the privileged `version-bump` job checks the patch it
produces by digest and against the bump plan before any private step.
The profile expects `cargo` in `$HOME/.cargo/bin`; without it, the version
bump of a Cargo project with a `Cargo.lock` fails.
