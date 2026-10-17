// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build !linux

package forgejobehavior

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

func TestMain(m *testing.M) {
	os.Exit(evidence.Main(m))
}

// The layer's shell runs on Linux runners and its tests are built for Linux
// only. Elsewhere the package would otherwise report "no test files", which a
// passing tier does not distinguish from a covered one.
func TestForgejoLayerShellNeedsLinux(t *testing.T) {
	t.Parallel()

	evidence.NotProvenf(t, "the Forgejo layer's shell tests run only on a native Linux host")
}
