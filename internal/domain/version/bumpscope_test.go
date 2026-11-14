// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

func TestPlannedBumpScope_OwnsOnlyTheProjectsNativeVersionFiles(t *testing.T) {
	t.Parallel()

	scopes := []version.BumpScope{
		{ProjectType: projecttype.Maven, WorkingDir: "services/backend"},
		{ProjectType: projecttype.NPM, WorkingDir: "packages/frontend"},
		{ProjectType: projecttype.Cargo, WorkingDir: "."},
		{ProjectType: projecttype.Go, WorkingDir: "tool"},
	}

	for file, want := range map[string]int{
		"services/backend/pom.xml":            0,
		"services/backend/api/pom.xml":        0, // a module POM below the project
		"packages/frontend/package.json":      1,
		"packages/frontend/package-lock.json": 1,
		"packages/frontend/lib/package.json":  -1, // npm owns only its own directory
		"Cargo.toml":                          2,
		"Cargo.lock":                          2,
		"tool/go.mod":                         -1, // Go has no native bump
		"services/backend/CHANGELOG.md":       -1,
		"other/pom.xml":                       -1,
	} {
		if got := version.PlannedBumpScope(scopes, file); got != want {
			t.Errorf("PlannedBumpScope(%q) = %d, want %d", file, got, want)
		}
	}
}
