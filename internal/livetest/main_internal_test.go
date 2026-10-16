// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"strings"
	"testing"
)

// TestMain gives the package one well-formed runner digest, whatever the
// process exports. Rendering a probe reads FORGE_CI_LIVE_RUNNER_SHA256 and panics
// without one, so these offline tests used to pass only on a host that
// happened to export it, and then rendered with that host's value. Tests about
// the digest itself set their own value. The live tier is another package and
// keeps the digest its launcher exports.
func TestMain(m *testing.M) {
	_ = os.Setenv(runnerBinarySHA256Env, strings.Repeat("0", 64))

	if os.Getenv("FORGE_CI_LIVE_SIGNAL_FIXTURE") != "" {
		os.Exit(RunSuite(m.Run))
	}

	os.Exit(m.Run())
}
