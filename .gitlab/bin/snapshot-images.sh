#!/usr/bin/env bash
# SPDX-FileCopyrightText: The Forge CI Authors
# SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

# Native GitLab transport only. Image policy and every executable container
# operation remain in `container snapshot-images`, shared with the other forges.
snapshot_gitlab_checkout() {
	local sha
	sha=$(snapshot_engine container snapshot-images read-plan --plan snapshot-plan.json --field source-sha) || return
	snapshot_checkout "${sha}"
}

snapshot_gitlab_build() {
	snapshot_gitlab_checkout
	if [ -n "${SNAPSHOT_CONTEXT_JOB:-}" ]; then
		snapshot_apply_context _snapshot-context snapshot-plan.json
	else
		snapshot_apply_context snapshot-context snapshot-plan.json
	fi
	snapshot_build "${SNAPSHOT_NAME:?}" "${SNAPSHOT_ARCH:?}"
	mv snapshot-source/snapshot-layouts snapshot-layouts
}

snapshot_gitlab_produce() {
	snapshot_gitlab_checkout
	_forge_ci_arch
	test "${HELPERS_ENGINE_ARCH}" = amd64
	snapshot_build_artifact "${SNAPSHOT_NAME:?}"
	mv snapshot-source/_snapshot-artifacts _snapshot-artifacts
}

snapshot_gitlab_gather() {
	snapshot_gitlab_checkout
	mkdir -p _snapshot-artifacts
	snapshot_gather_context "$(realpath -e _snapshot-artifacts)"
	mv snapshot-source/snapshot-context snapshot-context
}

snapshot_gitlab_publish() {
	# Checkout only reads the scanner policy; no source commands are executed.
	snapshot_gitlab_checkout
	if [ -f snapshot-source/.trivyignore.yaml ]; then cp snapshot-source/.trivyignore.yaml .; fi
	snapshot_evidence_tools "${SNAPSHOT_NAME:?}"
	SNAPSHOT_REGISTRY_USER=${SNAPSHOT_REGISTRY_USER:-${CI_REGISTRY_USER:-}}
	SNAPSHOT_REGISTRY_PASSWORD=${SNAPSHOT_REGISTRY_PASSWORD:-${CI_REGISTRY_PASSWORD:-}}
	snapshot_publish "${SNAPSHOT_NAME}" snapshot-layouts
}

# JSON is YAML: jq safely serializes names, branch inputs and runner tags. The
# generated native jobs extend a SHA-pinned template. Every publisher depends
# on ALL native builds, not just the architectures of its own image.
snapshot_gitlab_generate() {
	local plan=$1 output=$2
	local context=${SNAPSHOT_CONTEXT_JOB:-}
	if [ "${context}" = 'Snapshot: plan' ]; then context=; fi
	# The prefix forces GitLab's typed array input into a string variable;
	# without surrounding text interpolation preserves the YAML array type.
	local tags=${SNAPSHOT_RUNNER_TAGS:-[]} amd64=${SNAPSHOT_AMD64_TAGS:-[]} arm64=${SNAPSHOT_ARM64_TAGS:-[]}
	tags=${tags#json:}
	amd64=${amd64#json:}
	arm64=${arm64#json:}
	if [ "${CI_IMAGE_BUILDER:-buildah}" = kaniko ]; then
		[[ "${CI_IMAGE_BUILDER_JOB_IMAGE:-}" =~ @sha256:[0-9a-f]{64}$ ]]
		test "${SNAPSHOT_BUILD_IMAGE:?}" = "${CI_IMAGE_BUILDER_JOB_IMAGE}"
	fi
	jq -e --argjson arm64 "${arm64}" \
		'([.builds[] | select(.arch == "arm64")] | length == 0) or ($arm64 | length > 0)' "${plan}" >/dev/null || {
		echo 'ERROR: arm64 snapshot builds require native arm64 runner tags' >&2
		return 1
	}
	jq --arg project "${SNAPSHOT_LAYER_PROJECT:?}" --arg pin "${SNAPSHOT_ENGINE_SHA:?}" \
		--arg pipeline "${CI_PIPELINE_ID:?}" --arg context "${context}" \
		--arg job_image "${SNAPSHOT_JOB_IMAGE:?}" --arg build_image "${SNAPSHOT_BUILD_IMAGE:?}" \
		--arg builder "${CI_IMAGE_BUILDER:-buildah}" --arg kaniko_image "${CI_IMAGE_BUILDER_JOB_IMAGE:-}" \
		--arg go_image "${SNAPSHOT_ARTIFACT_IMAGE_GO:-${SNAPSHOT_JOB_IMAGE}}" \
		--arg java_image "${SNAPSHOT_ARTIFACT_IMAGE_JAVA:-${SNAPSHOT_JOB_IMAGE}}" \
		--arg node_image "${SNAPSHOT_ARTIFACT_IMAGE_NODE:-${SNAPSHOT_JOB_IMAGE}}" \
		--arg rust_image "${SNAPSHOT_ARTIFACT_IMAGE_RUST:-${SNAPSHOT_JOB_IMAGE}}" \
		--argjson tags "${tags}" --argjson amd64 "${amd64}" \
		--argjson arm64 "${arm64}" '
		def buildjob: "Snapshot: build " + .name + " " + .arch;
		def publishjob: "Snapshot: publish " + .name;
		def parent: {pipeline: $pipeline, job: "Snapshot: plan", artifacts: true};
		def producerjob: "Snapshot: artifact " + .name;
		def artifactimage:
		  if .project_type == "go" then $go_image
		  elif .project_type == "maven" or .project_type == "gradle" then $java_image
		  elif .project_type == "npm" then $node_image
		  elif .project_type == "cargo" then $rust_image
		  else error("unsupported snapshot artifact family: " + .project_type) end;
		. as $plan |
		(if $context == "" then ($plan.artifacts // []) else [] end) as $artifacts |
		($plan.builds | map({job: buildjob, artifacts: true})) as $buildneeds |
		{
		  include: [{project: $project, ref: $pin, file: "/templates/snapshot-image-jobs.yml", inputs: {
		    "engine-sha": $pin, "forge-ci-repository": $project,
		    "job-image": $job_image, "build-image": $build_image,
		    "runner-tags": $tags, "image-builder": $builder, "builder-job-image": $kaniko_image
		  }}],
		  stages: ["artifacts", "context", "build", "publish", "collect"]
		} +
		(reduce $artifacts[] as $a ({};
		  . + {($a | producerjob): {
		    extends: ".forge-ci-snapshot-produce", stage: "artifacts", tags: $amd64,
		    image: {name: ($a | artifactimage), entrypoint: [""]},
		    variables: {SNAPSHOT_NAME: $a.name}, needs: [parent]
		  }})) +
		(if $context == "" and ($plan.images | length > 0) then
		  {"Snapshot: context": {
		    extends: ".forge-ci-snapshot-context", stage: "context",
		    needs: ([parent] + ($artifacts | map({job: producerjob, artifacts: true})))
		  }} else {} end) +
		(reduce $plan.builds[] as $b ({};
		  . + {($b | buildjob): {
		    extends: ".forge-ci-snapshot-build", stage: "build",
		    tags: (if $b.arch == "arm64" then $arm64 else $amd64 end),
		    variables: {SNAPSHOT_NAME: $b.name, SNAPSHOT_ARCH: $b.arch, SNAPSHOT_CONTEXT_JOB: $context},
		    needs: ([parent] + if $context == "" then [{job: "Snapshot: context", artifacts: true}] else [{pipeline: $pipeline, job: $context, artifacts: true}] end)
		  }})) +
		(reduce $plan.images[] as $im ({};
		  . + {($im | publishjob): {
		    extends: ".forge-ci-snapshot-publish", stage: "publish",
		    variables: {SNAPSHOT_NAME: $im.name}, needs: ([parent] + $buildneeds)
		  }})) +
		{"Snapshot: collect": {
		  extends: ".forge-ci-snapshot-collect", stage: "collect",
		  needs: ([parent] + ($plan.images | map({job: publishjob, artifacts: true})))
		}}
	' "${plan}" >"${output}"
}
