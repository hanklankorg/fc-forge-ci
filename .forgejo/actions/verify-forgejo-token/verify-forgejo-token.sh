#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

set -euo pipefail

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
readonly FORGE_CI_EX_NOPERM=77

first_safe_log_line() {
	local raw="$1" line lower

	# Redact the submitted token and reject credential-bearing diagnostic forms.
	raw=${raw//"${FORGEJO_API_TOKEN}"/'[REDACTED]'}
	while IFS= read -r line || [ -n "${line}" ]; do
		line=${line//$'\r'/}
		line=$(LC_ALL=C tr -cd '\11\40-\176' <<<"${line}")
		# Redact again after control removal: a token split by CR or another
		# control character is contiguous only once those bytes are gone.
		line=${line//"${FORGEJO_API_TOKEN}"/'[REDACTED]'}
		[ -n "${line}" ] || continue

		lower=${line,,}
		case "${lower}" in
		*authorization* | *www-authenticate* | *cookie:* | *x-auth-token:* | \
			*private-token:* | *x-api-key:* | *http://*@* | *https://*@*)
			continue
			;;
		esac

		printf '%.400s\n' "${line}"
		return 0
	done <<<"${raw}"

	return 1
}

if [ -z "${FORGEJO_API_TOKEN:-}" ]; then
	echo "ERROR: forgejo API token is empty" >&2
	exit 1
fi

# shellcheck source=.forgejo/bin/lib-forge-ci.sh
source "${script_dir}/../../bin/lib-forge-ci.sh"

safe_repository=''
safe_repository=$(first_safe_log_line "${VERIFY_REPO:-}") || safe_repository=''
verify_server=${VERIFY_SERVER:-}
forge_ci_status=0
# Capture forge-ci stderr for classified failures while keeping its stdout
# quiet. Clear the exported action input from the child environment: stdin is
# the command's only token transport.
forge_ci_diagnostic=$(
	{
		printf '%s' "${FORGEJO_API_TOKEN}" |
			FORGEJO_API_TOKEN='' \
				FORGE_CI_PROVIDER=forgejo \
				FORGEJO_SERVER_URL="${verify_server%/}" \
				run_forge_ci validate auth token \
				--repository "${VERIFY_REPO:-}" \
				--token-file - >/dev/null
	} 2>&1
) || forge_ci_status=$?

if [ "${forge_ci_status}" -eq 0 ]; then
	if [ -n "${safe_repository}" ]; then
		echo "Forgejo API token verified for ${safe_repository}"
	else
		echo "Forgejo API token verified"
	fi
	exit 0
fi

# forge-ci's stable sysexits contract reserves EX_NOPERM for confirmed
# authentication or authorization rejection. Everything else is operational.
if [ "${forge_ci_status}" -eq "${FORGE_CI_EX_NOPERM}" ]; then
	if [ -n "${safe_repository}" ]; then
		echo "ERROR: forgejo API token was rejected or lacks read:repository scope for ${safe_repository}" >&2
	else
		echo "ERROR: forgejo API token was rejected or lacks read:repository scope" >&2
	fi
	exit 1
fi

if safe_diagnostic=$(first_safe_log_line "${forge_ci_diagnostic}"); then
	echo "ERROR: forgejo API token verification could not be completed (forge-ci exit ${forge_ci_status}): ${safe_diagnostic}" >&2
else
	echo "ERROR: forgejo API token verification could not be completed (forge-ci exit ${forge_ci_status}; no safe diagnostic was emitted)" >&2
fi
exit 1
