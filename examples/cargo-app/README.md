# Cargo application example

A Cargo workspace with two services (`worker`, `api-service`) shipped as
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
  [`forgejo/release.yml`](forgejo/release.yml), a binary release of the two
  services with one `build-cargo` step each, as on GitLab. Copy
  `artifacts.yml` to `.forge-ci/artifacts.yml`: the signed release commit
  carries the version of each crate it declares, which `prepare-release.yml`
  bumps in a key-free job in `runtime-image-rust`. The images are added to
  the build job as in the [Go service example](../go-service/forgejo/release.yml). See the
  [Forgejo guide](../../docs/forgejo.md#getting-started).

The rest of this page describes the GitHub configuration.

## Why this shape (container-first)

This example uses `config.build-mode: container-first` because its main
outputs are service images. Each Containerfile installs its own native
dependencies, such as `libssl-dev`, and the runtime image and the optional
extracted binary come from one compilation. For the `artifact-first`
alternative, see the
[Cargo ecosystem reference](../../docs/ecosystems.md#cargo-dual-mode).

## Files

- `artifacts.yml`: declares two `cargo` artefacts and two containers, one
  per service. `target: runtime` selects the runtime stage; `extract.binary`
  opts into binary upload.
- `Containerfile.example`: reference multi-stage Containerfile with the
  three named stages (`builder`, `export-binary`, `runtime`). Copy to
  `<service>/Containerfile` and adjust per-service.
- `github/pullrequest-workflow.yml`: runs forge-ci nanolinter checks and, in
  its own `test` job, `cargo test --workspace`; add feature combinations there.
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
   `/usr/local/cargo/registry` and `target/`.
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
podman build --target runtime --build-arg SERVICE=worker -t worker:dev .

# Extract just the binary
podman build --target export-binary -o ./out .
```

## Workspace tests

forge-ci does not run your tests. The `test` job in
`github/pullrequest-workflow.yml` runs `cargo test --workspace`; add feature
combinations and testcontainers runs there, for example
`cargo test --workspace --features worker/testcontainers,api-service/testcontainers -- --test-threads=1`.
Require that job and `pr-checks` as status checks.

## Toolchain pin

- **Required:** pin the toolchain in `rust-toolchain.toml` at every Cargo
  artefact's `working-directory`. `validate cargo` enforces this, and a
  release fails at the prerequisites stage without it.
- The GitLab example's check job runs `cargo fmt` and `cargo clippy`, so the
  pin must also list `components = ["rustfmt", "clippy"]`; the `minimal`
  profile installs neither.
- Every build cross-compiles each platform it selects, so the pin lists those
  targets too, for example `targets = ["x86_64-unknown-linux-gnu",
  "aarch64-unknown-linux-gnu"]` for GitLab's default Linux amd64 and arm64. A
  missing target fails with "can't find crate for `std`".

## Private cargo registry credentials (build-time)

If the workspace pulls from a private cargo registry, pass the token as a
build secret, not in `containers[].build-args`, which can leak into image
metadata and logs. Declare the name once:

```yaml
containers:
  - name: worker
    from: [worker]
    container-file: worker/Containerfile
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

The caller workflow passes the value in `FORGE_CI_BUILD_SECRETS_JSON`; see
[`build-secrets`](../../docs/artifacts-reference.md#build-secrets).

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
