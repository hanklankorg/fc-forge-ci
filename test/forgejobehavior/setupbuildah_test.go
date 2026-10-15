// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"slices"
	"testing"
)

// The setup step binds each input to the variable it reads and forwards them
// as engine flags, defaulting unset ones. Setup, package-list parsing and
// storage fallback belong to the engine and its companion suite.
func TestSetupBuildah(t *testing.T) {
	t.Parallel()
	action := loadAction(t, "setup-buildah")
	action.requireSteps(t, 1)
	script := action.Runs.Steps[0].Run

	t.Run("binds each step variable to its input", func(t *testing.T) {
		t.Parallel()

		action.requireStepEnv(t, 0, map[string]string{
			"SETUP_BUILDAH_EXTRA_PACKAGES":   "${{ inputs.extra-packages }}",
			"SETUP_BUILDAH_INSTALL_PACKAGES": "${{ inputs.install-packages }}",
			"SETUP_BUILDAH_PROBE_BUILD":      "${{ inputs.probe-build }}",
			"CONTAINER_STORAGE_PRINT_STORE":  "${{ inputs.print-store }}",
			"CONTAINER_STORAGE_SUMMARY":      "${{ inputs.summary }}",
		})
	})

	for _, test := range []struct {
		name string
		env  []string
		argv []string
	}{
		{
			"forwards explicit inputs and keeps the package list one value",
			[]string{
				"SETUP_BUILDAH_EXTRA_PACKAGES=jq\nskopeo", "SETUP_BUILDAH_INSTALL_PACKAGES=false", "SETUP_BUILDAH_PROBE_BUILD=true",
				"CONTAINER_STORAGE_PRINT_STORE=true", "CONTAINER_STORAGE_SUMMARY=false",
			},
			[]string{
				"container", "setup-buildah", "--install-packages=false", "--probe-build=true", "--print-store=true",
				"--summary=false", "--extra-packages", "jq\nskopeo",
			},
		},
		{
			"defaults unset inputs and passes empty extra packages",
			nil,
			[]string{
				"container", "setup-buildah", "--install-packages=true", "--probe-build=true", "--print-store=false",
				"--summary=false", "--extra-packages", "",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			s := newSandbox(t, "setup-buildah")

			got := s.run(script, test.env...)
			if got.status != 0 || !slices.Equal(s.argv(), test.argv) {
				t.Fatalf("status %d argv %q; stderr %s", got.status, s.argv(), got.stderr)
			}
		})
	}
}
