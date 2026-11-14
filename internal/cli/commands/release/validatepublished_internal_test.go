// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
)

// The GitHub release job names the tag, the repository, the draft setting
// and the assembly in its step's environment and passes no flag.
func TestValidatePublished_ReadsItsInputFromTheStepEnvironment(t *testing.T) {
	for _, draft := range []bool{true, false} {
		t.Setenv("TAG_NAME", "v1.2.3")
		t.Setenv("REPOSITORY", "owner/app")
		t.Setenv("RELEASE_ASSEMBLY", ".forge-ci/release-assembly.json")

		if draft {
			t.Setenv("DRAFT", "true")
		} else {
			t.Setenv("DRAFT", "false")
		}

		var got apprelease.ValidatePublishedInput

		cmd := validatePublishedCmd()
		cmd.Action = func(_ context.Context, cmd *cli.Command) error {
			got = validatePublishedInput(cmd)

			return nil
		}

		require.NoError(t, cmd.Run(t.Context(), []string{"validate-published"}))
		require.Equal(t, apprelease.ValidatePublishedInput{
			Tag: "v1.2.3", Repository: "owner/app", Draft: draft, AssemblyFile: ".forge-ci/release-assembly.json",
			Assets: []string{}, Files: got.Files,
		}, got)
	}
}
