// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli_test

import (
	"slices"
	"testing"

	urfavecli "github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/testutil/cliflags"
)

// The entire CLI shares these names. A leaf that retains an old spelling
// silently reads a different setting from the other stages of the same job.
func TestCommandEnvironmentUsesCanonicalNames(t *testing.T) {
	t.Parallel()

	retired := map[string]string{
		"REGISTRY_USERNAME":     "REGISTRY_USER",
		"RELEASE_IMAGES_LEDGER": "RELEASE_IMAGES_PATH",
		"CHANGELOG_PATH":        "CHANGELOG_FILE",
		"TOOLS_SUBSET":          "TOOLSET_TOOLS",
		"MISE_TOOLS":            "TOOLSET_TOOLS",
	}
	bindings := map[string][]string{}

	var walk func(*urfavecli.Command, string)

	walk = func(cmd *urfavecli.Command, path string) {
		for _, flag := range cmd.Flags {
			site := path + " --" + flag.Names()[0]
			bindings[site] = nil

			for _, source := range cliflags.Sources(t, flag).Chain {
				env, ok := source.(interface{ Key() string })
				if !ok {
					continue
				}

				bindings[site] = append(bindings[site], env.Key())
				if canonical, old := retired[env.Key()]; old {
					t.Errorf("%s %v reads retired %s; use %s", path, flag.Names(), env.Key(), canonical)
				}
			}
		}

		for _, sub := range cmd.Commands {
			walk(sub, path+" "+sub.Name)
		}
	}
	walk(cli.New(cli.BuildInfo{Version: "test"}), "forge-ci")

	// Check the actual stage bindings: finding one reader of a canonical name
	// would not catch another stage accidentally dropping its env source.
	for site, canonical := range map[string]string{
		"forge-ci container login --registry-username":          "REGISTRY_USER",
		"forge-ci container ledger add --ledger":                "RELEASE_IMAGES_PATH",
		"forge-ci container ledger validate --ledger":           "RELEASE_IMAGES_PATH",
		"forge-ci container release-images sign --ledger":       "RELEASE_IMAGES_PATH",
		"forge-ci release assemble-dist --release-images-path":  "RELEASE_IMAGES_PATH",
		"forge-ci version bump-plan --changelog-file":           "CHANGELOG_FILE",
		"forge-ci version render-changelog --changelog-path":    "CHANGELOG_FILE",
		"forge-ci version commit-changelog-release --changelog": "CHANGELOG_FILE",
		"forge-ci release validate-changelog --changelog-file":  "CHANGELOG_FILE",
		"forge-ci toolchain cache-discriminator --tools":        "TOOLSET_TOOLS",
		"forge-ci toolchain install-mise-tools --tools":         "TOOLSET_TOOLS",
		"forge-ci toolchain setup-go-env --tools":               "TOOLSET_TOOLS",
		"forge-ci toolchain validate-mise-install --tools":      "TOOLSET_TOOLS",
	} {
		if !slices.Equal(bindings[site], []string{canonical}) {
			t.Errorf("%s reads %v; want only %s", site, bindings[site], canonical)
		}
	}
}
