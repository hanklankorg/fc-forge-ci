#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
# SPDX-License-Identifier: CC0-1.0

# Run inside the candidate image, natively and without publication credentials.
# The caller supplies the Go version from the source Containerfile, not the image.
set -euo pipefail
target=${1:?runtime target required}
if [[ "$target" == go ]]; then
  expected_go=${2:?Go version from the source Containerfile required}
  [[ "$expected_go" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
    echo 'Invalid source Go version' >&2
    exit 1
  }
fi
bash --version
git --version
gpg --version
jq --version
yq --version
zip -v >/dev/null
git-cliff --version
gh --version
glab --version
mise --version
cosign version
forge-ci --version
test -d /opt/forge-ci/scripts/bootstrap
test -f /opt/forge-ci/templates/gitcliff/default.toml
case "$target" in
base) ;;
full)
  syft version
  trivy --version
  ;;
go)
  test "$(go env GOVERSION)" = "go$expected_go"
  gcc --version
  go test -race runtime/race
  cyclonedx-gomod version
  msgmerge --version
  msgen --version
  msgattrib --version
  msgfmt --version
  ;;
rust)
  rustc --version
  cargo --version
  cargo cyclonedx --version
  ;;
node)
  node --version
  ldconfig -p | grep -F libatomic.so.1
  npm --version
  corepack --version
  ;;
java | android)
  java -version
  gradle --version
  if [[ "$target" == java ]]; then
    mvn -version
  else
    sdkmanager --version
    adb --version
    test -d /opt/android-sdk/platforms
    test -d /opt/android-sdk/build-tools
  fi
  ;;
*)
  printf 'unknown runtime target: %s\n' "$target" >&2
  exit 1
  ;;
esac

# The same toolchain for the image's ordinary user, as a GitLab job with an
# image user runs it: HOME from /etc/passwd, every cache below it, and nothing
# outside it written but what the job installs, which lands on PATH in
# /opt/job-tools, and the rustup and Android SDK homes that install on demand.
# Root is the image's default, so the check drops to it.
nonroot_smoke() {
  local target=$1 work
  installed() {
    test "$(command -v -- "$1")" = "/opt/job-tools/bin/$1"
  }
  test "$(id -u):$(id -g)" = 65532:65532
  test "$HOME" = /home/nonroot
  work=$(mktemp -d "$HOME/smoke.XXXXXXXX")
  cd -- "$work"
  git init --quiet repository
  case "$target" in
  base | full) ;;
  go)
    printf 'module example.test/smoke\n\ngo 1.21\n' >go.mod
    printf 'package main\n\nfunc main() {}\n' >main.go
    go build -o smoke .
    case "$(go env GOMODCACHE) $(go env GOCACHE)" in
    "$HOME"/*" $HOME"/*) ;;
    *) echo 'Go caches are not in the user home' >&2 && return 1 ;;
    esac
    go install .
    installed smoke && smoke
    cyclonedx-gomod version
    ;;
  rust)
    cargo new --quiet --vcs none smoke
    cargo build --quiet --offline --manifest-path smoke/Cargo.toml
    test -d "$HOME/.cargo"
    cargo install --quiet --offline --path smoke
    installed smoke && smoke
    cargo cyclonedx --version
    test -w "$RUSTUP_HOME" && test -w "$RUSTUP_HOME/toolchains"
    ;;
  node)
    npm cache verify >/dev/null
    test -d "$HOME/.npm"
    corepack --version
    mkdir smoke
    printf '{"name":"smoke","version":"1.0.0","bin":{"smoke":"smoke.js"}}\n' >smoke/package.json
    printf '#!/usr/bin/env node\n' >smoke/smoke.js
    npm install --global --offline --no-audit --no-fund --silent ./smoke
    installed smoke && smoke
    ;;
  java | android)
    gradle --version >/dev/null
    test -d "$HOME/.gradle"
    # Maven's local repository is ~/.m2, which only a plugin run creates.
    mvn --batch-mode -version >/dev/null
    mkdir -p -- "$HOME/.m2/repository"
    [[ $target != android ]] || { test -w "$ANDROID_HOME/platforms" && test -w "$ANDROID_HOME/build-tools"; }
    ;;
  esac
  cd -- "$HOME"
  rm -rf -- "$work"
}
if [[ "$(id -u)" -eq 0 ]]; then
  home=$(getent passwd 65532 | cut -d: -f6)
  setpriv --reuid=65532 --regid=65532 --clear-groups \
    env HOME="$home" USER=nonroot LOGNAME=nonroot \
    bash -c "set -euo pipefail; $(declare -f nonroot_smoke); nonroot_smoke \"\$1\"" nonroot-smoke "$target"
fi
