// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaseVerificationWorkflowPassesOnlyExplicitAuth(t *testing.T) {
	t.Parallel()

	run := workflowStep(t, "verify-base-container-images.yml", "verify-base-container-images", "Verify existing base images").Run
	for _, mode := range []string{"anonymous", "authenticated", "missing-token", "login-failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			box := newSandbox(t, "base-auth")
			box.write(".engine/bin/forge-ci", `#!/usr/bin/env bash
set -euo pipefail
test -z "${REGISTRY_TOKEN:-}"
verb=$2
shift 2
file=
while (($#)); do
  if [ "$1" = --auth-file ]; then file=$2; shift; fi
  shift
done
case "$verb" in
login)
  token=$(cat)
  test "$token" = fixture-token
  test "$MODE" != login-failure || exit 73
  printf 'fixture-auth' >"$file"
  ;;
base-images)
  if [ "$MODE" = authenticated ]; then test "$(cat "$file")" = fixture-auth; else test -z "$file"; fi
  printf verified >"$SEEN"
  ;;
*) exit 99 ;;
esac
`, 0o700)

			for _, dir := range []string{"private", "consumer"} {
				if err := os.Mkdir(filepath.Join(box.root, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			token, user := "", ""
			if mode != "anonymous" {
				user = "owner"
			}

			if mode == "authenticated" || mode == "login-failure" {
				token = "fixture-token"
			}

			seen := filepath.Join(box.root, "verified")
			got := box.run(run, "HELPERS_DIR="+box.root, "SIGNER_WORKSPACE="+filepath.Join(box.root, "private"),
				"CONSUMER_WORKSPACE="+filepath.Join(box.root, "consumer"), "SEEN="+seen, "MODE="+mode,
				"TRUSTED_KEY_INPUT=fixture-key", "TRUSTED_KEY_SHA256_INPUT=fixture-digest",
				"EXPECTED_REPOSITORY=registry.example/owner/base", "FORGEJO_SERVER=https://forge.example", "FORGEJO_REPO=owner/app",
				"REGISTRY_TOKEN="+token, "REGISTRY_USER="+user)
			_, err := os.Stat(seen)

			wantSuccess := mode == "anonymous" || mode == "authenticated"
			if (got.status == 0) != wantSuccess || (err == nil) != wantSuccess {
				t.Fatalf("mode=%s status=%d verified=%v stderr=%s", mode, got.status, err, got.stderr)
			}

			if strings.Contains(got.stdout+got.stderr, "fixture-token") || strings.Contains(got.stdout+got.stderr, "fixture-auth") {
				t.Fatal("credentials escaped into diagnostics")
			}
		})
	}
}
