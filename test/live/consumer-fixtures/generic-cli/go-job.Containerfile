# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: CC0-1.0
ARG QUALITY_IMAGE
ARG GO_IMAGE
ARG JAVA_IMAGE
FROM ${GO_IMAGE} AS go-sdk
FROM ${JAVA_IMAGE} AS java-sdk
FROM ${QUALITY_IMAGE}
USER 0:0
COPY --from=go-sdk /usr/local/go /usr/local/go
COPY --from=java-sdk /opt/mise/installs/java/temurin-25.0.3+9.0.LTS /usr/local/jdk
COPY --chmod=0644 go-job.mise.toml /opt/forge-ci-job/config.toml
COPY nanolinter /usr/local/bin/nanolinter
COPY --chmod=0644 nano-acceptance.json /usr/local/share/forge-ci-job/nano-acceptance.json
ENV GOROOT=/usr/local/go GOTOOLCHAIN=local JAVA_HOME=/usr/local/jdk \
    PATH=/opt/mise/installs/aqua-casey-just/1.58.0:/usr/local/jdk/bin:/opt/mise/installs/node/24.17.0/bin:/usr/local/go/bin:/usr/lib/podman:/opt/mise/shims:/usr/local/bin:${PATH} \
    MISE_CONFIG_DIR=/opt/forge-ci-job \
    MISE_OVERRIDE_CONFIG_FILENAMES=/opt/forge-ci-job/config.toml
RUN test "$(go env GOVERSION)" = go1.27.3 \
    && printf '7169485ec40a07757b9545334e3a4b1fc4d2221b5135ba5d41aebee7b5d8308e  /usr/local/bin/nanolinter\n' | sha256sum --check \
    && chmod 0755 /usr/local/bin/nanolinter \
    && mise trust /opt/forge-ci-job/config.toml \
    && GOMAXPROCS=2 mise install \
    && mise reshim \
    && chown -R 65532:65532 /home/nanolinter
# Original integration tests require signing, ZIP and hermetic VFS build tools.
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends gpg gpg-agent buildah netavark aardvark-dns unzip \
    && rm -rf /var/lib/apt/lists/*
LABEL org.forge-ci.acceptance.nanolinter="source-built-signed-scratch-0.8.6-dev-c45719caf40b" \
      org.forge-ci.acceptance.nanolinter.sha256="7169485ec40a07757b9545334e3a4b1fc4d2221b5135ba5d41aebee7b5d8308e"
USER 65532:65532
WORKDIR /workspace
RUN test -r /opt/forge-ci-job/config.toml \
    && test "$(go env GOVERSION)" = go1.27.3 \
    && nanolinter --version \
    && govulncheck -version \
    && golangci-lint version \
    && go version -m "$(mise which govulncheck)" \
    && go version -m "$(mise which golangci-lint)" \
    && command -v gcc && command -v gpg && command -v gpg-agent \
    && gpg --version && command -v ssh-keygen
RUN test "$(command -v just)" = /opt/mise/installs/aqua-casey-just/1.58.0/just \
    && env -i HOME=/tmp PATH=/usr/bin:/bin /opt/mise/installs/aqua-casey-just/1.58.0/just --version \
    && test "$(node --version)" = v24.17.0 \
    && test "$(command -v node)" = /opt/mise/installs/node/24.17.0/bin/node \
    && test "$(command -v npm)" = /opt/mise/installs/node/24.17.0/bin/npm \
    && env -i HOME=/tmp PATH=/opt/mise/installs/node/24.17.0/bin:/usr/bin:/bin npm --version \
    && java -version \
    && keytool -list -cacerts -storepass changeit >/dev/null \
    && buildah --version \
    && test "$(command -v netavark)" = /usr/lib/podman/netavark \
    && test "$(command -v aardvark-dns)" = /usr/lib/podman/aardvark-dns \
    && netavark --version && aardvark-dns --version && unzip -v
