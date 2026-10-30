#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: CC0-1.0
set -euo pipefail
umask 077
: "${RELEASE_VERSION:?}" "${RELEASE_TAG:?}" "${RELEASE_SHA:?}"
test "$(git rev-parse HEAD)" = "${RELEASE_SHA}"
test "${RELEASE_TAG}" = "v${RELEASE_VERSION}"
test "$(uname -m)" = x86_64
test "$(uname -s)" = Linux
test ! -e dist
test ! -L dist
mkdir dist
kind=$(cat .gitlab/generic-cli-kind)
case "${kind}" in
go)
	export GOTOOLCHAIN=local GOOS=linux GOARCH=amd64
	test "$(go env GOVERSION)" = go1.27.3
	GOTOOLCHAIN=local go test ./...
	epoch=$(git show -s --format=%ct HEAD)
	build_date=$(date -u -d "@${epoch}" +%Y-%m-%dT%H:%M:%SZ)
	SOURCE_DATE_EPOCH="${epoch}" GOTOOLCHAIN=local go build -trimpath -buildvcs=false \
		-ldflags "-X main.version=${RELEASE_VERSION} -X main.commit=${RELEASE_SHA} -X main.date=${build_date}" \
		-o dist/forge-ci ./cmd/forge-ci
	binary=forge-ci
	;;
cargo)
	test "$(rustc --version | cut -d ' ' -f2)" = 1.99.1
	# Version preparation belongs to the existing isolated Cargo bump job.
	test "$(cargo metadata --locked --no-deps --format-version 1 | jq -r '.packages[] | select(.name == "nanolinter") | .version')" = "${RELEASE_VERSION}"
	# Retain blocking prepared-source bin/CI tests. The complete original gate
	# (including repository packaging-consistency guards and shell contracts)
	# already ran on the signed request before version preparation. These bin
	# commands do not select nanolinter's dependency_guards integration target.
	cargo test --locked --workspace --bins
	cargo test --locked --package nanolinter-ci --tests
	cargo build --locked --release --package nanolinter --bin nanolinter --target x86_64-unknown-linux-gnu
	cp -- target/x86_64-unknown-linux-gnu/release/nanolinter dist/nanolinter
	binary=nanolinter
	;;
*) exit 1 ;;
esac
test -f "dist/${binary}"
test ! -L "dist/${binary}"
# The source identity is the admitted source, not the generated adapter commit.
cp -- .gitlab/generic-cli-source.json dist/source-identity.json
jq -n --arg version "${RELEASE_VERSION}" --arg commit "${RELEASE_SHA}" \
	--arg binary "${binary}" --arg sha256 "$(sha256sum "dist/${binary}" | cut -d ' ' -f1)" \
	'{version:$version,preparedRelease:$commit,binary:$binary,sha256:$sha256}' >dist/build-identity.json
forge-ci release notes --changelog-file CHANGELOG.md --release-version "${RELEASE_TAG}" --target-file dist/release-notes.md
forge-ci release files declare --dist-dir dist --asset "dist/${binary}" \
	--asset dist/source-identity.json --asset dist/build-identity.json \
	--checksums-file dist/checksums.txt --evidence-file dist/release-notes.md
printf 'Release handoff files (bytes, path relative to dist):\n'
find dist -type f -printf '%s\t%P\n' | LC_ALL=C sort -k2
