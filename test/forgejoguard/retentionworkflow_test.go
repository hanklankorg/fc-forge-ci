// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

func TestCallableRetentionNeverAcquiresDeletionAuthority(t *testing.T) {
	t.Parallel()

	file := ".forgejo/workflows/package-retention.yml"
	body := string(reporoot.ReadFile(t, file))
	w := loadWorkflow(t, file)
	require.Empty(t, callableOnlyViolation([]byte(body)))
	require.Len(t, w.jobs, 1)
	require.Nil(t, mappingValue(w.jobs["preview"].node, "strategy"), "one Go pass must see the complete protected inventory, not per-pin matrix jobs")
	preview := w.jobs["preview"].node.String()
	require.Contains(t, preview, "--dry-run=true")
	require.NotContains(t, preview, "--dry-run=false")
	require.NotContains(t, preview, "go build")
	require.NotContains(t, preview, "platform checkout")
	require.Contains(t, preview, "sha256sum --check --strict")
	require.Contains(t, preview, "inputs['engine-sha256']")
	require.Contains(t, preview, "inputs['packages-json']")
	require.Contains(t, preview, "--packages-json")
	require.NotContains(t, preview, "matrix.digest")

	for _, forbidden := range []string{"itiquette", "secrets.RELEASE_COSIGN", "secrets.RELEASE_SSH", "secrets.RELEASE_GPG"} {
		require.NotContains(t, body, forbidden)
	}
}
