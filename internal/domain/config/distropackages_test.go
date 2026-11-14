// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func parseValidated(t *testing.T, body string) (*config.Config, error) {
	t.Helper()

	cfg, err := config.Parse([]byte("artifacts:\n  - name: cli\n    project-type: go\n" + body))
	if err != nil {
		return nil, err
	}

	return cfg, config.Validate(cfg)
}

func TestDistroPackagesDefaultEveryOmittedPlace(t *testing.T) {
	t.Parallel()

	// An empty block is the opt-in with every default.
	cfg, err := parseValidated(t, "distro-packages: {}\n")
	require.NoError(t, err)
	require.NotNil(t, cfg.DistroPackages)

	resolved := cfg.DistroPackages.Resolved()
	require.Equal(t, config.DebianRegistry{Distribution: "stable", Component: "main"}, resolved.Debian)
	require.Equal(t, config.RPMRegistry{Group: "stable"}, resolved.RPM)
	require.Equal(t, config.AlpineRegistry{Branch: "stable", Repository: "main"}, resolved.Alpine)

	// The declaration is not rewritten: only the resolved copy has defaults.
	require.Zero(t, cfg.DistroPackages.Debian)
}

func TestDistroPackagesKeepDeclaredPlaces(t *testing.T) {
	t.Parallel()

	cfg, err := parseValidated(t, `distro-packages:
  debian: {distribution: trixie, component: contrib}
  rpm: {group: rocky/el9}
  alpine: {branch: v3.22, repository: community}
`)
	require.NoError(t, err)

	resolved := cfg.DistroPackages.Resolved()
	require.Equal(t, config.DebianRegistry{Distribution: "trixie", Component: "contrib"}, resolved.Debian)
	require.Equal(t, config.RPMRegistry{Group: "rocky/el9"}, resolved.RPM)
	require.Equal(t, config.AlpineRegistry{Branch: "v3.22", Repository: "community"}, resolved.Alpine)
}

func TestDistroPackagesAbsentPublishesNone(t *testing.T) {
	t.Parallel()

	cfg, err := parseValidated(t, "")
	require.NoError(t, err)
	require.Nil(t, cfg.DistroPackages)
	require.Nil(t, cfg.DistroPackages.Resolved())
}

func TestDistroPackagesRefuseWhatIsNoRegistryPlace(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"a publish target":     "distro-packages:\n  publish-to: [forge-packages]\n",
		"path in distribution": "distro-packages:\n  debian: {distribution: ../other}\n",
		"slash in component":   "distro-packages:\n  debian: {component: main/extra}\n",
		"dot step in group":    "distro-packages:\n  rpm: {group: el9/../other}\n",
		"query in branch":      "distro-packages:\n  alpine: {branch: 'edge?sign=true'}\n",
		"uppercase repository": "distro-packages:\n  alpine: {repository: Main}\n",
		"unknown key":          "distro-packages:\n  debian: {suite: stable}\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := parseValidated(t, body)
			require.ErrorIs(t, err, errs.ErrInvalidConfig)
		})
	}
}

// A bare block is not an absent one: it would release without a package
// published, so it is refused with what to write for the defaults.
func TestDistroPackagesRefuseABareBlock(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"distro-packages:\n", "distro-packages: ~\n", "distro-packages: null\n"} {
		_, err := parseValidated(t, body)
		require.ErrorIs(t, err, errs.ErrInvalidConfig, body)
		require.ErrorContains(t, err, "write `distro-packages: {}`", body)
	}
}
