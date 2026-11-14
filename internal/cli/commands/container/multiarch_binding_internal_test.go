// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestMultiarchCommandsShareArchitectureEnvironmentAndFlagPrecedence(t *testing.T) {
	for _, makeCommand := range []func() *cli.Command{signerImageAssembleCmd, multiarchImageVerifyCmd} {
		for _, testCase := range []struct {
			name string
			env  string
			flag string
			want string
		}{
			{name: "arm64 environment", env: "arm64", want: "arm64"},
			{name: "both environment", env: "amd64\narm64", want: "amd64\narm64"},
			{name: "flag overrides environment", env: "arm64", flag: "amd64", want: "amd64"},
		} {
			command := makeCommand()
			t.Run(command.Name+"/"+testCase.name, func(t *testing.T) {
				env := testenv.New(t)
				env.Setenv("IMAGE_ARCHS", testCase.env)

				var got string

				command.Action = func(_ context.Context, parsed *cli.Command) error {
					got = parsed.String("archs")

					return nil
				}

				args := []string{command.Name}
				if command.Name == "verify" {
					args = append(args, "--ref", "fixture-ref")
				}

				if testCase.flag != "" {
					args = append(args, "--archs", testCase.flag)
				}

				require.NoError(t, command.Run(t.Context(), args))
				require.Equal(t, testCase.want, got)
			})
		}
	}
}
