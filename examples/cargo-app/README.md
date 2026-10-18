# Cargo application example

A Cargo workspace with two services (`hsm-worker`, `wallet-bff`) shipped as
separate container images. The example also shows how to extract the compiled
binaries as CI artefacts so they can be attached to a GitHub Release alongside
each container manifest.

## Per forge

- **GitHub**: [`github/pullrequest-workflow.yml`](github/pullrequest-workflow.yml)
  and [`github/release-workflow.yml`](github/release-workflow.yml), with
  `artifacts.yml`: the two service images. See the
  [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml), the lint gate,
  `check` and one `build-cargo` job per service. No component builds the
  images: in a release the policy project's `build` commands do. See the
  [GitLab guide](../../docs/gitlab.md#getting-started).
- **Forgejo**: [`forgejo/pullrequest.yml`](forgejo/pullrequest.yml) and
  [`forgejo/release.yml`](forgejo/release.yml), a `build-cargo` binary release
  of a crate at the repository root. An image is added to the build job as in
  the [Go service example](../go-service/forgejo/release.yml). See the
  [Forgejo guide](../../docs/forgejo.md#getting-started).

The rest of this page describes the GitHub configuration.

## Why this shape (container-first)

This example uses `config.build-mode: container-first` because its primary
outputs are service images. Each Containerfile owns native dependencies such
as `libssl-dev`; Buildah runs on native linux/amd64 and linux/arm64 runners.
The runtime image and optional extracted binary share one compilation.
`sbom-cargo.yml` reads `Cargo.lock` with `cargo cyclonedx` without compiling,
and the container scan follows each service's effective SBOM selection.
Workspace tests, Clippy/formatting and RUSTSEC policy stay in the caller's
`test.yml`, as the example below shows.

For the full lifecycle comparison, cross-linker requirements, standalone CLI
output layout and `artifact-first` alternative, use the
[Cargo ecosystem reference](../../docs/ecosystems.md#cargo-dual-mode).

## Files

- `artifacts.yml`: declares two `cargo` artefacts and two containers, one
  per service. `target: runtime` selects the runtime stage; `extract.binary`
  opts into binary upload.
- `Containerfile.example`: reference multi-stage Containerfile with the
  three named stages (`builder`, `export-binary`, `runtime`). Copy to
  `<service>/Containerfile` and adjust per-service.
- `github/pullrequest-workflow.yml`: runs forge-ci nanolinter checks.
  Add a caller-owned `test.yml` for workspace-specific Rust checks.
- `github/release-workflow.yml`: standard signed `release-request/vX.Y.Z`-driven
  release; the orchestrator creates the final `vX.Y.Z` tag after preparation,
  then dispatches `sbom-cargo` per artefact and `publish-container` per container.
  When `extract.binary` is set, each container's compiled binary is uploaded
  as `${name}-binaries-${arch}` per platform leg and aggregated into the
  GitHub Release.

## Containerfile pattern

The reference `Containerfile.example` uses three named stages:

1. **`builder`**: installs native deps via apt, `COPY`s workspace sources,
   runs `cargo build --release` with `--mount=type=cache` for
   `/usr/local/cargo/registry` and `target/`. Buildah may reuse those mounts on
   a persistent worker; forge-ci's cross-run CI cache is the separate
   registry layer cache (`--cache-from`/`--cache-to`), not a forge-native cache
   backend.
2. **`export-binary`**: `FROM scratch`, just `COPY --from=builder` the
   binaries to root. Used by `extract.binary.target: export-binary` to
   write the binary to disk during container publish.
3. **`runtime`**: Debian-slim base, runtime deps installed via apt,
   non-root user, `COPY --from=builder` the binary, `ENTRYPOINT`. This is
   what gets pushed to the registry.

Build context is the workspace root (`context: .` in `artifacts.yml`),
so `COPY Cargo.toml Cargo.lock` and per-crate `COPY` statements resolve
against workspace-level paths.

The Containerfile is written for Buildah/Podman. CI invokes daemonless Buildah;
Docker is not required. For local development with Podman:

```bash
# Build the runtime image
podman build --target runtime --build-arg SERVICE=hsm-worker -t hsm-worker:dev .

# Extract just the binary
podman build --target export-binary -o ./out .
```

CI uses `forge-ci container build`, backed by Buildah and a tagless OCI push.
Local Podman builds work without Docker installed.

## Workspace tests live in the caller's `test.yml`

Workspace-level features and testcontainers can't be expressed per-artefact,
so they live in the caller. The forge-ci PR orchestrator does not invoke
this workflow automatically; call it from your own PR workflow if you need it.

```yaml
# .github/workflows/test.yml
name: Cargo Test
on: [workflow_call]
permissions:
  contents: read
jobs:
  test:
    runs-on: ubuntu-26.04
    steps:
      - uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10 # v6.0.3
      - run: rustup show active-toolchain >/dev/null 2>&1 || rustup toolchain install
      - uses: actions/cache@27d5ce7f107fe9357f9df03efb73ab90386fccae # v5.0.5
        with:
          path: |
            ~/.cargo/registry
            ~/.cargo/git
            target
          key: cargo-test-${{ runner.os }}-${{ hashFiles('Cargo.lock') }}
      - run: |
          cargo test --workspace --features hsm-worker/testcontainers,wallet-bff/testcontainers \
            -- --test-threads=1
```

**Required:** pin the toolchain via `rust-toolchain.toml` at every Cargo
artefact's `working-directory`. `validate cargo` enforces this: a
release will fail at the prerequisites stage if the pin is missing.
`sbom-cargo.yml` auto-detects the file directly.

## Private cargo registry credentials (build-time)

If the workspace pulls from a private cargo registry, **do not** put the token
in `containers[].build-args`: build arguments are not a secret channel and can
be exposed by Containerfile instructions, image metadata, or build logs.

Use `containers[].build-secrets` instead. Declare the name once:

```yaml
containers:
  - name: hsm-worker
    from: [hsm-worker]
    container-file: hsm-worker/Containerfile
    context: .
    build-secrets:
      - PRIVATE_CARGO_REGISTRY_TOKEN
```

Consume it in the `builder` stage with a tmpfs mount:

```dockerfile
RUN --mount=type=secret,id=private_cargo_registry_token,target=/run/secrets/cargo-token \
    CARGO_REGISTRIES_INTERNAL_TOKEN="$(cat /run/secrets/cargo-token)" \
    cargo build --release
```

The caller workflow forwards a single `FORGE_CI_BUILD_SECRETS_JSON`
envelope; see [docs/artifacts-reference.md `build-secrets`](../../docs/artifacts-reference.md#build-secrets)
for the full recipe.

## Adding a third service

1. New `artifacts:` entry with `project-type: cargo`, its
   `working-directory`, and `config.build-mode: container-first` (this
   example wraps each crate in its own Containerfile; for a standalone
   CLI binary release, use `artifact-first` instead; see the
   [dual-mode comparison](../../docs/ecosystems.md#cargo-dual-mode)).
2. New `containers:` entry referencing the artefact by name, pointing at
   `<service>/Containerfile`. Include `target:` and `extract:` if you want
   the binary as a CI artefact.
3. New `<service>/Containerfile` (copy `Containerfile.example` and adjust
   the COPY paths and which binary is built).
4. The release matrix expands automatically, with no orchestrator changes
   needed.
