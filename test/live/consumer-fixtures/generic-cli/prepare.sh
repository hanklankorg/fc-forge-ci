#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: CC0-1.0
# Operator-only: create an unsigned disposable adapter snapshot; never a request tag.
set -euo pipefail
umask 077
# Do not let caller Git routing/configuration redirect writes out of the clone
# or install checkout hooks/filters through ambient user configuration.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
source_root=${1:?absolute clean source root}
destination=${2:?absolute NEW disposable checkout}
kind=${3:?go or cargo}
expected=${4:?full reviewed source commit}
test "$#" -ge 4 && test "$#" -le 5
forge=${5:-gitlab}
case "${forge}" in gitlab | forgejo) ;; *) exit 1 ;; esac
case "${source_root}:${destination}" in /*:/*) ;; *) exit 1 ;; esac
case "${kind}" in go | cargo) ;; *) exit 1 ;; esac
test ! -e "${destination}"
test ! -L "${destination}"
test -d "$(dirname -- "${destination}")"
destination=$(realpath -m -- "${destination}")
# A clone inside the source would itself dirty the admitted checkout.
case "${destination}/" in "${source_root}/"*) exit 1 ;; esac
test "$(git -C "${source_root}" rev-parse --show-toplevel)" = "${source_root}"
test -z "$(git -C "${source_root}" status --porcelain --untracked-files=all)"
test "$(git -C "${source_root}" rev-parse HEAD)" = "${expected}"
format=$(git -C "${source_root}" rev-parse --show-object-format)
case "${format}" in
sha1) [[ "${expected}" =~ ^[0-9a-f]{40}$ ]] ;;
sha256) [[ "${expected}" =~ ^[0-9a-f]{64}$ ]] ;;
*) exit 1 ;;
esac
for path in .gitlab .chglog .forgejo .forgejo/workflows; do
	mode=$(git -C "${source_root}" ls-tree "${expected}" -- "${path}" | cut -d ' ' -f1)
	test -z "${mode}" || test "${mode}" = 040000
done
recipe=$(dirname -- "$(realpath -- "$0")")
engine=$(realpath -- "${recipe}/../../../..")
# No source tar, source writes or symlink traversal: Git objects are borrowed;
# every added blob, index entry and commit is owned by the disposable clone.
git -c core.hooksPath=/dev/null clone --shared --no-checkout -- "${source_root}" "${destination}"
git -C "${destination}" remote remove origin
test "$(git -C "${destination}" rev-parse --show-object-format)" = "${format}"
git -C "${destination}" read-tree "${expected}"
stage() {
	local path=$1 blob
	blob=$(git -C "${destination}" hash-object -w --stdin)
	git -C "${destination}" update-index --add --cacheinfo "100644,${blob},${path}"
}
stage_license() {
	printf 'SPDX-FileCopyrightText: %s\nSPDX-License-Identifier: CC0-1.0\n' "$2" | stage "$1.license"
}
stage .gitlab/build-release.sh <"${recipe}/build-release.sh"
stage .gitlab/check-release.sh <"${recipe}/check-release.sh"
printf '%s\n' "${kind}" | stage .gitlab/generic-cli-kind
if [ "${kind}" = go ]; then
	printf '%s\n' buildah-integration | stage .gitlab/generic-cli-checks
	stage_license .gitlab/generic-cli-checks 'The Forge CI Authors'
fi
jq -n --arg commit "${expected}" --arg objectFormat "$(git -C "${source_root}" rev-parse --show-object-format)" \
	'{commit:$commit,objectFormat:$objectFormat,lintPolicy:"informational",excludedChecks:[],coverageLimitation:"Full Nanolinter plan runs diagnostically; its findings are not an acceptance gate. Prior findings, including 64 KICS findings, remain unresolved. Application tests/build/release/signatures/payload verification remain blocking; not a full original just verify pass."}' | stage .gitlab/generic-cli-source.json
for path in .gitlab/generic-cli-kind .gitlab/generic-cli-source.json; do
	stage_license "${path}" 'The Forge CI Authors'
done
for path in .chglog/config-keepachangelog.yml .chglog/CHANGELOG-keepachangelog.tpl .chglog/config-minimal.yml .chglog/CHANGELOG-minimal.tpl; do
	stage "${path}" <"${engine}/test/live/demos/hello-maven/consumer/${path}"
done
# Templates were covered by the engine's demo annotation, which does not travel
# with this overlay. Carry their original attribution explicitly.
for path in .chglog/CHANGELOG-keepachangelog.tpl .chglog/CHANGELOG-minimal.tpl; do
	stage_license "${path}" '2026 Digg - Agency for Digital Government'
done
if [ "${forge}" = forgejo ]; then
	# The native trial admits only this release workflow, not unrelated source
	# workflows pinned to a different engine. Remove them only from our index.
	git -C "${destination}" ls-files .forgejo/workflows | while IFS= read -r path; do
		git -C "${destination}" update-index --force-remove -- "${path}"
	done
	workflow=$(<"${recipe}/forgejo-release.yml")
	engine_sha=$(git -C "${engine}" rev-parse HEAD)
	workflow=${workflow//ENGINE_SHA/${engine_sha}}
	if [ "${kind}" = cargo ]; then
		workflow=${workflow//VERSION_BUMP/cargo}
		workflow=${workflow//BUMP_IMAGE/\$\{\{ vars.CI_JOB_IMAGE \}\}}
		workflow=${workflow//IS_GO/false}
	else
		workflow=${workflow//VERSION_BUMP/\'\'}
		workflow=${workflow//BUMP_IMAGE/\'\'}
		workflow=${workflow//IS_GO/true}
	fi
	printf '%s\n' "${workflow}" | stage .forgejo/workflows/release.yml
fi
tree=$(git -C "${destination}" write-tree)
commit=$(printf 'test: disposable generic CLI release adapter\n' |
	git -C "${destination}" -c user.name='CLI fixture' -c user.email=fixture@localhost commit-tree "${tree}")
# read-tree populated the index, not the worktree. A non-forced checkout
# preserves these missing files as local deletions. Force is confined to this
# newly created private clone, whose destination was required to be absent.
git -C "${destination}" -c core.hooksPath=/dev/null checkout --force --detach "${commit}"
test -z "$(git -C "${destination}" status --porcelain --untracked-files=all)"
test "$(git -C "${source_root}" rev-parse HEAD)" = "${expected}"
test -z "$(git -C "${source_root}" status --porcelain --untracked-files=all)"
printf 'Prepared %s from %s (%s); no release request created\n' "${destination}" "${expected}" "${kind}"
