// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package report_test

import (
	"testing"

	reportcmd "github.com/diggsweden/forge-ci/internal/cli/commands/report"
	"github.com/diggsweden/forge-ci/internal/testutil/ghaenv"
	"github.com/stretchr/testify/require"
)

func TestSnapshotReportReadsImageResultAndInventoryEnvironment(t *testing.T) {
	env := ghaenv.Setup(t)
	env.Setenv("SNAPSHOT_IMAGE_RESULT", "failure")
	env.Setenv("CONTAINER_IMAGES_JSON", "[]")
	require.NoError(t, reportcmd.New().Run(t.Context(), []string{"report", "snapshot-release"}))
	require.Contains(t, env.Summary(), "| Snapshot Images | ✗ |")
	env.Setenv("CONTAINER_IMAGES_JSON", "null")
	require.Error(t, reportcmd.New().Run(t.Context(), []string{"report", "snapshot-release"}))
}
