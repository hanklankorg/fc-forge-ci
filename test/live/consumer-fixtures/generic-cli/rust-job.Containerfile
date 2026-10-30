# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: CC0-1.0

# Operator supplies the reviewed immutable native Rust tool-image identity.
# This job-role layer adds request verification/build prerequisites; product
# runtime flavors remain unchanged. It contains no signing credentials.
ARG SOURCE_IMAGE
FROM ${SOURCE_IMAGE}
USER 0:0
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends openssh-client gettext libgettextpo-dev procps \
    && rm -rf /var/lib/apt/lists/*
USER 65532:65532
WORKDIR /workspace
# The source image carries the matching KICS bundle here, while its PATH
# executable is copied into /usr/local/bin rather than next to those assets.
ENV KICS_QUERIES_PATH=/opt/kics/assets/queries
# Bound Cargo package compilation on shared acceptance hosts. Keep application
# codegen/release profiles and test-thread policy unchanged.
ENV CARGO_BUILD_JOBS=2
COPY --chmod=0555 verify-rust-job.sh /usr/local/bin/verify-generic-cli-rust-job
RUN bash /usr/local/bin/verify-generic-cli-rust-job
