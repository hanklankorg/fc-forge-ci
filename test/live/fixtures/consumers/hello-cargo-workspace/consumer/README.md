# Hello Cargo workspace

Two independently runnable binaries, two image targets and two binary export
targets from one dependency-free Cargo workspace. Both crates inherit the root
workspace version. Each executable reports its name and compiled version, so
release verification can detect a stale bump or swapped image/binary.

Run `cargo test --workspace --locked --offline`. The Containerfile runs the same
tests before building both release binaries. The GitHub release configuration
selects both runtime and export targets on Linux amd64 only.

The Forgejo and GitLab releases export both binaries from the `export-binaries`
stage, one native job per platform `RELEASE_PLATFORMS` selects (`linux/amd64`
when unset), with the image builder the organization selects, Buildah or
kaniko. They build the two runtime images `.forge-ci/release-images.yml`
declares, published to the release's one image repository as the tag variants
`<tag>-worker` and `<tag>-keytool`. The version in `Cargo.toml` stays as
committed: the release tag names the release.
