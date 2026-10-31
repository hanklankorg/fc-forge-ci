// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// Prove the private branch API used by runtime promotion accepts the actual
// runner-issued token, not just the controller's more capable lab credential.
func TestInRunner_PrivateSourceBranchAcceptsJobToken(t *testing.T) {
	livetest.RegisterLiveScenario(t)
	for _, forge := range livetest.ForgesMeeting(t, []provider.ForgeAPI{provider.ForgeForgejo}, livetest.NeedsInRunner) {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()

			target := livetest.Accept(t, forge)
			repo := livetest.NewBareScratchRepo(t, target, "private-source-token", "sha1", true)
			livetest.CommitFiles(t, target, repo, livetest.FileCommit{
				Branch: "main", Message: "test: initialize private source probe",
				Files: map[string]string{"README.md": "Private source-token probe.\n"},
			})

			labels, err := json.Marshal([]string{livetest.RunnerLabel(t, target)})
			if err != nil {
				t.Fatal(err)
			}

			workflow := strings.NewReplacer(
				"PROBE_LABELS", string(labels), "PROBE_IMAGE", livetest.ProbeImage,
				"TRUST_LAB_CA", strings.ReplaceAll(livetest.TrustLabCA(target), "\n", "\n          "),
			).Replace(privateSourceTokenProbe)
			livetest.EnableActions(t, target, repo)

			if result := livetest.RunWorkflow(t, target, repo, "private-source-token", workflow); result != "success" {
				t.Fatalf("private source branch job-token probe concluded %s", result)
			}
		})
	}
}

const privateSourceTokenProbe = `on: [push]
jobs:
  private-source:
    runs-on: PROBE_LABELS
    container: PROBE_IMAGE
    timeout-minutes: 5
    steps:
      - name: Verify anonymous refusal and job-token access
        shell: bash
        env:
          FORGEJO_TOKEN: ${{ forgejo.token }}
          SOURCE_SERVER: ${{ forgejo.server_url }}
          SOURCE_REPOSITORY: ${{ forgejo.repository }}
        run: |
          set -euo pipefail
          TRUST_LAB_CA
          source_token=${FORGEJO_TOKEN:?}
          export -n source_token
          unset FORGEJO_TOKEN
          url="$SOURCE_SERVER/api/v1/repos/$SOURCE_REPOSITORY/branches/main"
          anonymous=$(curl --proto '=https' --silent --show-error --max-time 30 \
            --output /dev/null --write-out '%{http_code}' "$url")
          case "$anonymous" in
            401|403|404) ;;
            *) echo "Private source was not denied anonymously: $anonymous" >&2; exit 1 ;;
          esac
          authenticated=$(printf 'header = "Authorization: token %s"\n' "$source_token" |
            curl --config - --proto '=https' --fail --silent --show-error --max-time 30 \
              --output /dev/null --write-out '%{http_code}' "$url")
          test "$authenticated" = 200
          echo 'Private source branch: anonymous denied, job token accepted'
`
