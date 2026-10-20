# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
#
# The GitLab release build image: the job image's Node and npm, plus Buildah
# and Trivy for the runtime image. It runs as root, as GitLab's build-image
# may; the image it builds copies the package's files and runs nothing, so
# Buildah needs no RUN isolation here.
FROM ghcr.io/aquasecurity/trivy:0.76.0@sha256:2416d1f74045b57274df1a340eee6b510cacd73c66a6ccb8da092b117eb7d222 AS trivy

FROM docker.io/library/node:24-trixie-slim@sha256:7f486cabe892bff8fef04c0f7da8dba84a560be3e4e2740b7a86afc57ebec315

USER 0:0

# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends git jq curl ca-certificates openssh-client bash coreutils findutils buildah netavark \
    && rm -rf /var/lib/apt/lists/* \
    && printf '[storage]\ndriver = "vfs"\nrunroot = "/run/containers/storage"\ngraphroot = "/var/lib/containers/storage"\n' \
        >/etc/containers/storage.conf

COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy

# Buildah inside a container: vfs storage and chroot isolation need no
# privileged mounts.
ENV STORAGE_DRIVER=vfs BUILDAH_ISOLATION=chroot

RUN node --version && npm --version && buildah --version && trivy --version
