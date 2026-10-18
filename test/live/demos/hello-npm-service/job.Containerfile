# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: CC0-1.0

# The hello-npm-service demo's job image on GitLab: the release's own jobs and
# the project's checks run in it, as a non-root user, with Git, ssh-keygen (the
# release verifies the SSH-signed request), curl and jq beside the Node and npm
# the package needs. `just demo-image hello-npm-service <dir>` builds it and
# saves the archive `just demo gitlab` takes as job-archive=.
FROM docker.io/library/node:24-trixie-slim@sha256:7f486cabe892bff8fef04c0f7da8dba84a560be3e4e2740b7a86afc57ebec315

USER 0:0

# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends git jq curl ca-certificates openssh-client bash coreutils findutils \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 nonroot \
    && useradd --uid 65532 --gid 65532 --home-dir /home/nonroot --create-home --shell /bin/bash nonroot

ENV HOME=/home/nonroot
USER 65532:65532
WORKDIR /home/nonroot

RUN node --version && npm --version && git --version && command -v ssh-keygen
