// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const bootstrapSourceCommit = "e91643249bc852cda21aa53d57d55d1d5d743205"

// Relative reusable-workflow calls use the caller's exact scratch snapshot.
// The selected engine source is deliberately separate from that candidate copy.
const githubBootstrapWorkflow = `name: Bootstrap identity smoke
on:
  workflow_dispatch:
permissions:
  contents: read
jobs:
  explicit:
    uses: ./.github/workflows/resolve-forge-ci-bootstrap.yml
    with:
      forge-ci-binary-ref: SOURCE_COMMIT
  stable:
    uses: ./.github/workflows/resolve-forge-ci-bootstrap.yml
    with:
      forge-ci-binary-ref: ''
  verify:
    name: Verify bootstrap source and stable selection
    needs: [explicit, stable]
    runs-on: ubuntu-26.04
    timeout-minutes: 15
    steps:
      - name: Checkout the exact candidate snapshot
        uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10
        with:
          ref: ${{ github.sha }}
          persist-credentials: false
      - uses: actions/setup-go@4a3601121dd01d1626a1e23e37211e3254c1c06c
        with:
          go-version: '1.27.3'
          cache: false
      - name: Check snapshot, consume stable tag, install and inspect exact source
        env:
          SOURCE_SHA: ${{ needs.explicit.outputs.forge-ci-binary-ref }}
          STABLE_REF: ${{ needs.stable.outputs.forge-ci-binary-ref }}
          STABLE_RUNTIME_REF: ${{ needs.stable.outputs.runtime-image }}
          EXPLICIT_RUNTIME_REF: ${{ needs.explicit.outputs.runtime-image }}
          SNAPSHOT_SHA: ${{ github.sha }}
          GH_TOKEN: ${{ github.token }}
          FORGE_CI_REPOSITORY: diggsweden/forge-ci
          FORGE_CI_SERVER_URL: https://github.com
          GOTOOLCHAIN: local
          GOFLAGS: -buildvcs=true
        run: |
          set -euo pipefail
          test "$(uname -m)" = x86_64
          test "$(go env GOVERSION)" = go1.27.3
          test "$(git rev-parse HEAD)" = "${SNAPSHOT_SHA}"
          sha256sum --check bootstrap-files.sha256
          test "${SOURCE_SHA}" = SOURCE_COMMIT
          [[ "${STABLE_REF}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]
          test "${STABLE_RUNTIME_REF}" = "ghcr.io/diggsweden/forge-ci-runtime-base:${STABLE_REF}"
          test "${EXPLICIT_RUNTIME_REF}" = "ghcr.io/diggsweden/forge-ci-runtime-base:sha-${SOURCE_SHA:0:7}"
          # Consume the resolved concrete tag, never resolve latest a second time.
          selected=$(gh api "repos/${FORGE_CI_REPOSITORY}/releases/tags/${STABLE_REF}" \
            --jq 'select(.draft == false and .prerelease == false) | .tag_name')
          test "${selected}" = "${STABLE_REF}"
          export FORGE_CI_SOURCE_SHA="${SOURCE_SHA}"
          export FORGE_CI_INSTALL_DIR="${RUNNER_TEMP}/bootstrap-source-bin"
          bash scripts/bootstrap/install-forge-ci.sh "${SOURCE_SHA}"
          binary="${FORGE_CI_INSTALL_DIR}/forge-ci"
          go version -m "${binary}" > build-info.txt
          grep -F "vcs.revision=${SOURCE_SHA}" build-info.txt
          grep -F 'vcs.modified=false' build-info.txt
          "${binary}" validate tag release-guard --release-policy stable --tag v1.2.3
          digest=$(sha256sum "${binary}")
          printf 'BOOTSTRAP_RUNTIME stable=%s explicit=%s\n' "${STABLE_RUNTIME_REF}" "${EXPLICIT_RUNTIME_REF}"
          printf 'BOOTSTRAP_RESULT source=%s stable=%s binary=%s snapshot=%s tree=%s\n' \
            "${SOURCE_SHA}" "${STABLE_REF}" "${digest%% *}" "${SNAPSHOT_SHA}" "$(git rev-parse 'HEAD^{tree}')"
`

var bootstrapResultPattern = regexp.MustCompile(`(?m)BOOTSTRAP_RESULT source=([0-9a-f]{40}) stable=(v[0-9]+\.[0-9]+\.[0-9]+) binary=([0-9a-f]{64}) snapshot=([0-9a-f]{40}) tree=([0-9a-f]{40})\s*$`)

func bootstrapResult(log, snapshot, tree string) (string, string, error) {
	matches := bootstrapResultPattern.FindAllStringSubmatch(log, -1)
	if len(matches) != 1 || matches[0][1] != bootstrapSourceCommit || matches[0][4] != snapshot || matches[0][5] != tree {
		return "", "", errors.New("bootstrap result must bind one actual source install to the candidate snapshot and tree")
	}

	return matches[0][2], matches[0][3], nil
}

// Classic PATs advertise their scopes on authenticated API responses. Refuse a
// known inability to push workflow files or clean up before creating a repo.
// Fine-grained tokens omit this header; their actual resource permissions still
// face the existing scoped SDK and provider checks.
func bootstrapClassicScopes(scopes string) error {
	if scopes == "" {
		return nil
	}

	granted := make(map[string]bool)
	for scope := range strings.SplitSeq(scopes, ",") {
		granted[strings.TrimSpace(scope)] = true
	}

	if !granted["workflow"] || !granted["delete_repo"] {
		return errors.New("bootstrap scratch test requires an existing PAT with workflow and delete_repo scope; no repository created")
	}

	return nil
}

func TestGitHubBootstrapClassicScopesRefuseKnownCleanupOrWorkflowDenial(t *testing.T) {
	for scopes, admitted := range map[string]bool{
		"":                                 true,
		"repo, workflow, delete_repo":      true,
		"public_repo,workflow,delete_repo": true,
		"repo,read:packages":               false,
		"repo,workflow":                    false,
		"repo,delete_repo":                 false,
	} {
		if err := bootstrapClassicScopes(scopes); (err == nil) != admitted {
			t.Errorf("admitted=%t for scope metadata %q: %v", admitted, scopes, err)
		}
	}
}

func TestGitHubBootstrapResultRequiresBoundEvidence(t *testing.T) {
	snapshot, tree := strings.Repeat("b", 40), strings.Repeat("c", 40)
	line := "BOOTSTRAP_RESULT source=" + bootstrapSourceCommit + " stable=v0.90.0 binary=" + strings.Repeat("a", 64) + " snapshot=" + snapshot + " tree=" + tree

	for _, tc := range []struct {
		name, log string
		valid     bool
	}{
		{"actual result", line, true},
		{"timestamped result", "2026-10-28T00:00:00Z " + line + "\n", true},
		{"absent", "", false},
		{"duplicate", line + "\n" + line, false},
		{"wrong source", strings.ReplaceAll(line, bootstrapSourceCommit, strings.Repeat("d", 40)), false},
		{"wrong snapshot", strings.ReplaceAll(line, snapshot, strings.Repeat("d", 40)), false},
		{"wrong tree", strings.ReplaceAll(line, tree, strings.Repeat("d", 40)), false},
		{"moving alias", strings.ReplaceAll(line, "v0.90.0", "latest"), false},
		{"prerelease", strings.ReplaceAll(line, "v0.90.0", "v0.90.0-pre"), false},
		{"shell source", "printf 'BOOTSTRAP_RESULT source=%s stable=%s binary=%s snapshot=%s tree=%s\\n'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := bootstrapResult(tc.log, snapshot, tree)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
		})
	}
}

func TestGitHubBootstrapWorkflowHasOnlyReadOnlyNativeSmokeJobs(t *testing.T) {
	var workflow struct {
		On          map[string]any    `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Uses    string            `yaml:"uses"`
			With    map[string]string `yaml:"with"`
			Runner  string            `yaml:"runs-on"`
			Timeout int               `yaml:"timeout-minutes"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(githubBootstrapWorkflow), &workflow); err != nil {
		t.Fatal(err)
	}

	if _, ok := workflow.On["workflow_dispatch"]; !ok || len(workflow.On) != 1 || len(workflow.Jobs) != 3 || len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		t.Fatal("bootstrap fixture must be dispatch-only with read-only contents and three jobs")
	}

	for _, name := range []string{"explicit", "stable"} {
		if workflow.Jobs[name].Uses != "./.github/workflows/resolve-forge-ci-bootstrap.yml" {
			t.Fatal("resolver must come from the caller's exact candidate snapshot")
		}
	}

	if workflow.Jobs["explicit"].With["forge-ci-binary-ref"] != "SOURCE_COMMIT" || workflow.Jobs["stable"].With["forge-ci-binary-ref"] != "" || workflow.Jobs["verify"].Runner != "ubuntu-26.04" || workflow.Jobs["verify"].Timeout != 15 {
		t.Fatal("bootstrap fixture changed explicit/default selection or bounded native runner")
	}
}
