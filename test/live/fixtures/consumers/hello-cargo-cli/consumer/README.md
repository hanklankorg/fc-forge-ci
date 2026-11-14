# Hello Cargo CLI

A tiny Rust command, a demo for the live tests, built without a
container: `hello-cargo-cli Ada` prints `Hello, Ada!`. The greeting comes
from `hello-greeting`, a library crate under `greeting/` in the same
workspace. Neither uses anything but the standard library, so the build
fetches no crate.

The declaration names the root package once, `build-mode: artifact-first`:
cargo compiles the command in the build job, and the release ships the
binary with the Build SBOM of its lockfile. The library crate is linked into
the binary and declared nowhere. A Cargo artifact is a binary: a crate
without one, declared on its own, fails its build at `cargo build --bin`.

The release bumps the root package in `Cargo.toml` and `Cargo.lock`; the
library crate keeps its own version. It builds `linux/amd64` alone, since the
toolchain pin lists no cross target.

Its workflows name Forge CI where it is published, at a placeholder commit;
`just demo` puts the Forge CI under test in its place.
