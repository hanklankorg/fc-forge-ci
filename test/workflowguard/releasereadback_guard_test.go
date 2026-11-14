// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The readback is the last step of the job that publishes the GitHub
// release. It reads a published release with the job's own token and no
// secret; a draft, which GitHub hides from that token, with the release token
// that created it.
func TestPublishedReleaseIsReadBackWithTheJobToken(t *testing.T) {
	t.Parallel()

	steps := readCanonicalConsumerWorkflow(t, "release-create-github.yml").Jobs["create-release"].Steps
	readback := steps[len(steps)-1]

	require.Contains(t, readback.Run, "forge-ci release validate-published")
	require.Empty(t, readback.If)

	tokens := 0

	for name, value := range readback.Env {
		if strings.HasSuffix(name, "_TOKEN") {
			require.Equal(t, "${{ inputs.draft && secrets.RELEASE_TOKEN || github.token }}", value)

			tokens++

			continue
		}

		require.NotContains(t, value, "secrets.", name)
	}

	require.Equal(t, 1, tokens)
}
