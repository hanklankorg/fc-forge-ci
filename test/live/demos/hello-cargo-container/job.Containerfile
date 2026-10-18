# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: CC0-1.0

# The hello-cargo-container demo's job image on GitLab: the release's own jobs
# and the project's checks run in it, as a non-root user, with Git,
# ssh-keygen (the release verifies the SSH-signed request), curl and jq
# beside the Rust toolchain the tests need. `just demo-image
# hello-cargo-container <dir>` builds it and saves the archive `just demo
# gitlab` takes as job-archive=.
FROM docker.io/library/rust:1.99.0-slim-trixie@sha256:2752b332db73fdbb7dc576f06c82ed1f312005784ef913d7e04a28f5f55dc581

USER 0:0

# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends git jq curl ca-certificates openssh-client bash coreutils findutils \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 nonroot \
    && useradd --uid 65532 --gid 65532 --home-dir /home/nonroot --create-home --shell /bin/bash nonroot

# The toolchain stays in the image's read-only RUSTUP_HOME; crates and
# build state go to the user's own CARGO_HOME.
ENV CARGO_HOME=/home/nonroot/.cargo HOME=/home/nonroot
USER 65532:65532
WORKDIR /home/nonroot

RUN cargo --version && rustc --version && git --version && command -v ssh-keygen
