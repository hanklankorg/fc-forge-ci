#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: CC0-1.0

# Prints the frozen plan of one stage of base images for base-images.yml.
#
#   base-plan.sh <flavor>[,<flavor>...] <image-repository> [<parent-ref> <parent-input-id>]
#
# Without a parent the stage is one root. With one, every flavor named is a
# child of that parent, and several are its siblings, built by one call.
#
# An image is built from images/<flavor>/, its Containerfile and what it
# copies. Its input ID is the SHA-256 of those files and, for a child, of the
# parent's input ID: a change to the parent gives the child a new ID and a new
# content-addressed tag. The graph, the IDs and the tags are this project's
# policy; forge-ci only requires that a plan is one complete stage.
set -euo pipefail
export LC_ALL=C

IFS=, read -r -a flavors <<<"${1:?flavors}"
repository=${2:?image repository}
parent_ref=${3:-} parent_input_id=${4:-}
if [[ -n "${parent_ref}" && ! "${parent_ref}${parent_input_id}" =~ ^[a-z0-9.:/_-]+@sha256:[0-9a-f]{64}[0-9a-f]{64}$ ]]; then
  echo "ERROR: a child names its parent's digest-pinned ref and its input ID" >&2
  exit 1
fi

lineage='' build_args='' registry_inputs=''
if [[ -n "${parent_ref}" ]]; then
  lineage=$(printf ',"provenance-json":"{\\"parent_base\\":{\\"ref\\":\\"%s\\",\\"input_id\\":\\"%s\\"}}"' "${parent_ref}" "${parent_input_id}")
  build_args="PARENT_IMAGE=${parent_ref}"
  registry_inputs=$(printf ',"registry-inputs":{"PARENT_IMAGE":"%s"}' "${parent_ref}")
fi

images='' builds=''
for flavor in "${flavors[@]}"; do
  if [[ ! "${flavor}" =~ ^[a-z][a-z0-9-]*$ || ! -f "images/${flavor}/Containerfile" ]]; then
    echo "ERROR: images/${flavor}/Containerfile names no base image" >&2
    exit 1
  fi
  input_id=$({
    find "images/${flavor}" -type f -print0 | sort -z | xargs -0 sha256sum
    printf '%s\n' "${parent_input_id}"
  } | sha256sum | cut -d ' ' -f 1)
  tag="${input_id}-${flavor}"
  images+=$(printf '%s{"name":"%s","image-repository":"%s","candidate-tag":"staging-%s","final-tag-name":"%s","archs":"amd64","image-kind":"base","flavor":"%s","base-input-id":"%s"%s}' \
    "${images:+,}" "${flavor}" "${repository}" "${tag}" "${tag}" "${flavor}" "${input_id}" "${lineage}")
  builds+=$(printf '%s{"name":"%s","arch":"amd64","platform":"linux/amd64","container-file":"images/%s/Containerfile","context":"images/%s","target":"","build-args":"%s"%s,"image-repository":"%s","candidate-tag":"staging-%s","final-tag-name":"%s"}' \
    "${builds:+,}" "${flavor}" "${flavor}" "${flavor}" "${build_args}" "${registry_inputs}" "${repository}" "${tag}" "${tag}")
done

printf '{"images":[%s],"builds":[%s]}\n' "${images}" "${builds}"
