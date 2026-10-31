// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"strings"
	"testing"
)

// TestMain supplies an offline rendering default without replacing the actual
// runner digest exported by a live launcher. Digest-specific tests set their own.
func TestMain(m *testing.M) {
	if os.Getenv(runnerBinarySHA256Env) == "" {
		_ = os.Setenv(runnerBinarySHA256Env, strings.Repeat("0", 64))
	}

	// Live entrypoints and offline signal fixtures share the normal lifecycle.
	os.Exit(RunSuite(m.Run))
}
