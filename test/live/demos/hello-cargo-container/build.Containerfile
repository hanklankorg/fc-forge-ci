# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0
#
# The GitLab release build image: the job image's Rust for the tests, plus
# Buildah and Trivy for the image. It runs as root, as GitLab's build-image
# may. The Containerfile it builds RUNs cargo in its builder stage, so
# Buildah runs that step with chroot isolation inside this container, with
# no privilege the job container lacks. Debian's Buildah needs netavark to
# create a build container, even with host networking.
FROM ghcr.io/aquasecurity/trivy:0.75.0@sha256:af6acf9a6b85dfe389a1941505c0ce9efef52a4719635e1a962f022a3d855daa AS trivy

FROM docker.io/library/rust:1.99.0-slim-trixie@sha256:2752b332db73fdbb7dc576f06c82ed1f312005784ef913d7e04a28f5f55dc581

USER 0:0

# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends git jq curl ca-certificates openssh-client bash coreutils findutils buildah netavark \
    && rm -rf /var/lib/apt/lists/* \
    && printf '[storage]\ndriver = "vfs"\nrunroot = "/run/containers/storage"\ngraphroot = "/var/lib/containers/storage"\n' \
        >/etc/containers/storage.conf

COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy

# Buildah inside a container: vfs storage and chroot isolation need no
# privileged mounts. With STORAGE_DRIVER alone, buildah push after a
# multi-stage build fails on an overlay driver; storage.conf above names vfs.
ENV STORAGE_DRIVER=vfs BUILDAH_ISOLATION=chroot

RUN cargo --version && buildah --version && trivy --version
