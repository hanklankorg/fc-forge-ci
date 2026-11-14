// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package runcontext

import (
	"os"
	"strings"
)

// TempRoot returns the runner's per-job temp dir, falling back to the OS temp
// dir off a runner. Scratch files belong there so the runner removes them.
func TempRoot(getenv func(string) string) string {
	if dir := TempDir().Resolve(getenv); strings.TrimSpace(dir) != "" {
		return dir
	}

	return os.TempDir()
}
