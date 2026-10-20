# Hello Cargo workspace

Two independently runnable binaries, two image targets and two binary export
targets from one dependency-free Cargo workspace. Both crates inherit the root
workspace version. Each executable reports its name and compiled version, so
release verification can detect a stale bump or swapped image/binary.

Run `cargo test --workspace --locked --offline`. The Containerfile runs the same
tests before building both release binaries. The GitHub release configuration
selects both runtime and export targets on Linux amd64 only.

Live acceptance must pull both published images and execute both extracted
binaries, checking their names and requested release version. Local tests and
workflow rendering are not evidence of image publication. Forgejo/GitLab
container-first workspace workflows and dev releases remain pending.
