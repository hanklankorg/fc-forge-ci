// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"path"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// BumpScope is one project of a version-bump plan: its type and its
// repository-relative working directory.
type BumpScope struct {
	ProjectType projecttype.Type
	WorkingDir  string
}

// PlannedBumpScope returns the index of the planned project whose native
// version file file is, or -1. A file belongs to a project when it is one of
// the type's BumpPatchFiles in the project's directory; a Maven project also
// owns the POMs of its modules below it. The first matching project wins, so
// the plan's order decides between nested projects.
func PlannedBumpScope(scopes []BumpScope, file string) int {
	for index, scope := range scopes {
		files, supported := BumpPatchFiles(scope.ProjectType)
		if !supported {
			continue
		}

		dir := path.Clean(scope.WorkingDir)
		if slices.Contains(files, path.Base(file)) && (path.Dir(file) == dir ||
			scope.ProjectType == projecttype.Maven && (dir == "." || strings.HasPrefix(file, dir+"/"))) {
			return index
		}
	}

	return -1
}
