# Hello Cargo Container

A tiny Rust service for showing Forge CI on Forge Lab, built container-first:
the `Containerfile` is the build. Its builder stage compiles the service with
`cargo build --release --locked`, the `runtime` stage is the image, and the
`export-binary` stage holds only the binary, which the release extracts and
ships as a file beside the image.

The service answers `GET /` with a greeting and `GET /health` with `ok`, on
port 8080. It uses the standard library only, so the image build fetches no
crate. It handles no signal, and as a container's first process it ignores
SIGTERM: `podman run --init` stops it at once.

The builder runs cargo for the platform it builds, so each architecture
needs a native runner; the releases build `linux/amd64`. The version in
`Cargo.toml` stays as committed: the release tag names the release.

Its workflows name Forge CI where it is published, at a placeholder commit;
`just demo` puts the Forge CI under test in its place.
