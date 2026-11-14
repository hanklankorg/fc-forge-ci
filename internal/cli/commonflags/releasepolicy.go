// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package commonflags

import "github.com/urfave/cli/v3"

// ReleasePolicy is supplied by the trusted release workflow, not build outputs.
func ReleasePolicy() *cli.StringFlag {
	return &cli.StringFlag{
		Name: "release-policy", Value: "stable", Sources: cli.EnvVars("RELEASE_POLICY"),
		Usage: "accepted release versions: stable or stable-prerelease",
	}
}
