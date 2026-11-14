// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah

import "github.com/diggsweden/forge-ci/internal/domain/container"

// ignoreFileArgs names the ignore file of containerfile to buildah explicitly,
// container.OwnIgnoreFile's choice. It returns nil when there is none, and the
// build context's own .containerignore or .dockerignore applies as before.
//
// Buildah 1.40 and later prefer a Containerfile's own ignore file over the
// context's, as BuildKit does for a Dockerfile's; Buildah 1.39 (Debian 13)
// reads only the context's. A repository whose root allowlist suits its local
// builds and whose release Containerfile carries its own allowlist then built
// a different context on each version, and on 1.39 a COPY of a file only the
// release allowlist admits failed. Passing the file makes every version build
// the context the Containerfile's author wrote.
//
// The path is resolved as buildah resolves --file: as given, relative to the
// process working directory.
func ignoreFileArgs(containerfile string) []string {
	if own := container.OwnIgnoreFile(containerfile); own != "" {
		return []string{"--ignorefile", own}
	}

	return nil
}
