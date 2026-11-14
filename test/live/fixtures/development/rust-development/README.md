<!-- SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Rust development fixture

The `consumer/` directory is a complete, dependency-free Rust sample for
`TestGitLabLayer_RustDevelopment` and `TestForgejoLayer_RustDevelopment`.
It builds four small executables with fixed, neutral names:

| Executable | Runtime image | Path in image |
|---|---|---|
| `worker` | `worker` | `/worker` |
| `key-tool` | `worker` | `/usr/local/bin/key-tool` |
| `api-service` | `api-service` | `/api-service` |
| `load-generator` | `load-generator` | `/usr/local/bin/load-generator` |

No separate role configuration or external application checkout is required.
These programs identify themselves; they do not claim to implement a real
service or load-testing system.

## Local source verification

On native Linux/AMD64 with Rust, Cargo and a C linker installed:

```sh
cd test/live/fixtures/development/rust-development/consumer
bash ci/build.sh
```

This runs the Rust tests, builds static executables and executes all four.
Cargo runs locked and offline; there are no external crates. This command does
not build or publish container images.

## Live provider execution

Read [the live-run instructions](../../../../../docs/live-runs.md) before
scheduling provider work. Copy `consumer/` to a fresh disposable directory,
excluding `target/`, initialize a Git repository there and commit its files.
Set `FORGE_CI_LIVE_CONSUMER` to that clean checkout's absolute path. This is the
same clean-source interface used by other development scenarios, not a new
fixture-selection mechanism.

The controller replaces the neutral engine coordinates and job-image variables
in the workflows with the selected engine commit and admitted job image.
The placeholders are not directly runnable deployment coordinates.

- GitLab: select `TestGitLabLayer_RustDevelopment` and provide the existing
  `FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE` build-image input.
- Forgejo: select `TestForgejoLayer_RustDevelopment` and provide the existing
  digest-pinned `CI_RUST_BUILD_IMAGE`. The controller supplies the builder and
  disposable registry credentials through its normal inputs.

The native job image needs Bash, Git, Cargo/Rust, a C linker with static libc,
`sha256sum`, the selected image builder and the engine's scanner tools.
It must satisfy the selected builder's existing prerequisites. Engine setup
uses the controller's pinned engine. Container builds and publication belong
on the approved remote runners, not the developer machine.

## Evidence contract

`ci/produce.sh` tests and compiles the binaries, builds and scans three scratch
runtime images through `forge-ci container build --mode oci`, records each
layout, and extracts the executable bytes from those same layouts. Only after
all three succeed does it publish the layouts by digest, without registry tags.

The provider retains exactly eight files under `development-artifacts/`:

- `receipt.json`: source commit, `0.1.0-dev.<12-char SHA>` version and SHA-256
  hashes of the other seven files. `ci/receipt.sh` executes the fixture's static
  Rust receipt helper, built before the image builds and retained in the
  workspace. It uses the surviving BusyBox `sha256sum`, not `jq`, and writes
  the receipt atomically after all seven hashes succeed.
- `worker-image.json`, `api-service-image.json`, `load-generator-image.json`:
  engine-generated layout records.
- `linux-amd64/<name>-linux-amd64` for each of the four executables above.

The Go acceptance checks independently enforce these fixed names, source and
version bindings, checksums, ELF architecture, image-byte equality, distinct
image digests, bounded archives and absence of publication tags. Offline
regressions also reject tampered bytes, wrong commits and unexpected files.

This scenario currently verifies Linux/AMD64 only. Passing its local tests is
not hosted acceptance or ARM64 evidence. No hosted run is implied by the
presence of the fixture.
