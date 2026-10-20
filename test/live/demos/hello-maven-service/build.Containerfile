# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
#
# The GitLab release build image: the job image's Maven and Java, plus Buildah
# and Trivy for the runtime image. It runs as root, as GitLab's build-image
# may; the image it builds copies a jar and runs nothing, so Buildah needs no
# RUN isolation here.
FROM ghcr.io/aquasecurity/trivy:0.76.0@sha256:2416d1f74045b57274df1a340eee6b510cacd73c66a6ccb8da092b117eb7d222 AS trivy

FROM docker.io/library/maven:3.9-eclipse-temurin-25@sha256:96f175708f766ea049ab6c794a85335d238894c75f8eb364bddd98086a5b7cfd

USER 0:0

RUN apt-get update \
    && apt-get install -y --no-install-recommends git jq curl ca-certificates openssh-client bash coreutils findutils buildah netavark \
    && rm -rf /var/lib/apt/lists/* \
    && printf '[storage]\ndriver = "vfs"\nrunroot = "/run/containers/storage"\ngraphroot = "/var/lib/containers/storage"\n' \
        >/etc/containers/storage.conf

COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy

# Buildah inside a container: vfs storage and chroot isolation need no
# privileged mounts.
ENV STORAGE_DRIVER=vfs BUILDAH_ISOLATION=chroot

RUN java -version && mvn -v && buildah --version && trivy --version
