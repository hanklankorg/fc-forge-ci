// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package syncguard

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// TestRunByTableInSync asserts that internal/cli/runby.json, the RUN BY
// section of --help, matches what cmd/gen-runby finds in the workflows now.
// A workflow that starts or stops calling a command makes that help wrong.
//
// On failure, run `just gen-runby` to refresh the file.
func TestRunByTableInSync(t *testing.T) {
	t.Parallel()

	want, err := cli.GenerateRunBy(reporoot.Path(t))
	require.NoError(t, err)

	path := "internal/cli/runby.json"
	got := reporoot.ReadFile(t, path)
	require.Empty(t, generatedDifference(path, "just gen-runby", got, []byte(want)))
}
