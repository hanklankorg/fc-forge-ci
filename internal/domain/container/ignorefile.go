// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"os"
	"path/filepath"
)

// ignoreSuffixes are the ignore file names in the order they win:
// .containerignore ahead of .dockerignore, as Buildah and Podman read them.
var ignoreSuffixes = []string{".containerignore", ".dockerignore"}

// OwnIgnoreFile returns the ignore file containerfile carries beside it,
// <containerfile>.containerignore, else <containerfile>.dockerignore, when it
// is a regular file, and "" when it has none. The path is containerfile's with
// the suffix appended, resolved as the builder resolves the Containerfile.
func OwnIgnoreFile(containerfile string) string {
	if containerfile == "" {
		return ""
	}

	for _, suffix := range ignoreSuffixes {
		if isRegularFile(containerfile + suffix) {
			return containerfile + suffix
		}
	}

	return ""
}

// IgnoreFile returns the ignore file that applies to a build of containerfile
// in contextDir: the Containerfile's own (OwnIgnoreFile), else the context's
// .containerignore, else its .dockerignore, and "" when none applies. The
// Containerfile's own wins over the context's, as in Buildah 1.40 and later and
// BuildKit, so one repository can keep a root allowlist for its local builds
// and a different one per release Containerfile.
func IgnoreFile(containerfile, contextDir string) string {
	if own := OwnIgnoreFile(containerfile); own != "" {
		return own
	}

	for _, suffix := range ignoreSuffixes {
		if candidate := filepath.Join(contextDir, suffix); isRegularFile(candidate) {
			return candidate
		}
	}

	return ""
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}
