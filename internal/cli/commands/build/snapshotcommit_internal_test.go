// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"context"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

// Exercise the real build commands' flag binding: the selected checkout can
// differ from the manual dispatch's policy commit. Empty workflow inputs must
// still leave the existing provider-event fallback available to direct callers.
func TestSnapshotBuildCommitOverridesPolicyEventAndEmptyFallsBack(t *testing.T) {
	for _, factory := range []struct {
		name string
		new  func() *cli.Command
	}{{"go", goRunCmd}, {"cargo", cargoRunCmd}} {
		for _, frozen := range []bool{true, false} {
			name := "empty input"
			if frozen {
				name = "frozen source"
			}
			t.Run(factory.name+"/"+name, func(t *testing.T) {
				env := testenv.New(t)
				source, policy := strings.Repeat("a", 40), strings.Repeat("b", 40)
				env.Setenv("GITHUB_SHA", policy)
				env.Setenv("CI_COMMIT", "")
				want := policy
				if frozen {
					env.Setenv("CI_COMMIT", source)
					want = source
				}
				command := factory.new()
				called := false
				command.Action = func(_ context.Context, cmd *cli.Command) error {
					called = true
					require.Equal(t, want, cmd.String("commit"), "value consumed by the build action")
					return nil
				}
				require.NoError(t, command.Run(t.Context(), []string{"run"}))
				require.True(t, called)
			})
		}
	}
}
