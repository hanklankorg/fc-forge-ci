// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestEventPolicyBoundary_RejectsCommentsAndBypasses(t *testing.T) {
	t.Parallel()

	const head = "on:\n  workflow_call:\npermissions:\n  packages: write\njobs:\n  publish:\n    steps:\n"

	const secret = "      - env:\n          TOKEN: ${{ github.token }}\n        run: publish\n"
	for _, guard := range []string{"# forge-ci validate event-context", "false && forge-ci validate event-context", "echo forge-ci validate event-context", "forge-ci validate event-context || true"} {
		body := head + "      - run: |\n          " + guard + "\n" + secret
		require.NotEmpty(t, eventContextViolations([]byte(body), false), guard)
	}

	good := head + "      - run: forge-ci validate event-context\n" + secret
	require.Empty(t, eventContextViolations([]byte(good), false))

	for _, condition := range []string{"always()", "AlWaYs ()", "success() || true", "failure()"} {
		bypass := strings.Replace(good, "      - env:", "      - if: "+condition+"\n        env:", 1)
		require.NotEmpty(t, eventContextViolations([]byte(bypass), false), condition)
	}

	require.NotEmpty(t, eventContextViolations([]byte(good), true), "forwarder may not consume secrets directly")

	for _, bypass := range []string{"        if: false\n", "        continue-on-error: true\n", "        shell: bash {0} || true\n"} {
		body := head + "      - run: forge-ci validate event-context\n" + bypass + secret
		require.NotEmpty(t, eventContextViolations([]byte(body), false))
	}

	readonly := strings.Replace(head, "packages: write", "contents: read", 1) + secret
	require.Empty(t, eventContextViolations([]byte(readonly), false), "read-only GitHub token is not a publishing credential")

	presence := head + "      - if: always()\n        env:\n          HAS_KEY: ${{ secrets.SIGN_KEY != '' }}\n        run: report\n"
	require.Empty(t, eventContextViolations([]byte(presence), false))
	require.NotEmpty(t, eventContextViolations([]byte(strings.Replace(presence, "!= ''", "!= '' && secrets.SIGN_KEY", 1)), false))
}

func TestEventPolicyBoundary_RequiresGuardDependencies(t *testing.T) {
	t.Parallel()

	const (
		head   = "on:\n  workflow_call:\njobs:\n  guard:\n    steps:\n      - run: forge-ci validate event-context\n  publish:\n"
		secret = "    steps:\n      - env:\n          TOKEN: ${{ secrets.SIGN_KEY }}\n        run: publish\n"
	)
	require.NotEmpty(t, eventContextViolations([]byte(head+secret), false))
	require.Empty(t, eventContextViolations([]byte(head+"    needs: guard\n"+secret), false))
	require.NotEmpty(t, eventContextViolations([]byte(head+"    needs: guard\n    if: always()\n"+secret), false))
	transitive := strings.Replace(head, "  publish:\n", "  bridge:\n    needs: guard\n    steps:\n      - run: true\n  publish:\n    needs: bridge\n", 1)
	require.Empty(t, eventContextViolations([]byte(transitive+secret), false))
}

func TestSupplyChainBoundary_PinsEveryExternalUse(t *testing.T) {
	t.Parallel()

	for _, use := range []string{"actions/checkout@main", "org/repo/.github/workflows/build.yml@v1", "docker://alpine:latest"} {
		body := []byte("jobs:\n  job:\n    uses: " + use + "\n")
		require.NotEmpty(t, supplyChainViolations(body))
	}

	for _, use := range []string{"actions/checkout@" + strings.Repeat("a", 40), "./.github/workflows/build.yml", "docker://registry.example/app@sha256:" + strings.Repeat("b", 64)} {
		require.Empty(t, supplyChainViolations([]byte("jobs:\n  job:\n    steps:\n      - uses: "+use+"\n")))
	}

	const action = "runs:\n  using: composite\n  steps:\n    - uses: https://code.forgejo.org/actions/cache@"
	require.NotEmpty(t, supplyChainViolations([]byte(action+"v3\n")))
	require.Empty(t, supplyChainViolations([]byte(action+strings.Repeat("c", 40)+"\n")))

	require.NotEmpty(t, supplyChainViolations([]byte("permissions: write-all\njobs: {}\n")))
	require.NotEmpty(t, supplyChainViolations([]byte("permissions: read-all\njobs:\n  job:\n    permissions: write-all\n")))
}

func TestWorkflowBoundary_DirectivesActionsAndRemote(t *testing.T) {
	t.Parallel()

	for _, directive := range []string{"from", "FrOm", "FROM\t"} {
		require.NotEmpty(t, unpinnedRuntimeStages(directive+" alpine:latest\n"))
	}

	require.Empty(t, unpinnedRuntimeStages("from alpine@sha256:"+strings.Repeat("a", 64)+" AS base\nFROM base\n"))

	for _, action := range []string{"actions/upload-artifact", "Actions/Upload-Artifact", "ACTIONS/UPLOAD-ARTIFACT"} {
		body := "jobs:\n  job:\n    steps:\n      - uses: " + action + "@pinned\n        with:\n          path: .\n"
		require.Len(t, auditUploadArtifactPaths(t, "fixture.yml", []byte(body)), 1)
	}
}
