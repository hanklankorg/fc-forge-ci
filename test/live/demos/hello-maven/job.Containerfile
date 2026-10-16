# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: CC0-1.0

# The hello-maven demo's job image on GitLab: the release's own jobs and the
# project's checks run in it, as a non-root user, with Git, ssh-keygen (the
# release verifies the SSH-signed request), curl and jq beside the JDK and
# Maven the POM needs. `just demo-image hello-maven <dir>` builds it and saves
# the archive `just demo gitlab` takes as job-archive= and build-archive=.
FROM docker.io/library/maven:3.9-eclipse-temurin-25@sha256:96f175708f766ea049ab6c794a85335d238894c75f8eb364bddd98086a5b7cfd

USER 0:0

# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends git jq curl ca-certificates openssh-client bash coreutils findutils \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 nonroot \
    && useradd --uid 65532 --gid 65532 --home-dir /home/nonroot --create-home --shell /bin/bash nonroot

ENV MAVEN_CONFIG=/home/nonroot/.m2 HOME=/home/nonroot
USER 65532:65532
WORKDIR /home/nonroot

RUN java -version && mvn -v && git --version && command -v ssh-keygen
