// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git_test

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

// The SSH signing tests skip without ssh-keygen, and they are the only place
// the allowed-signers trust boundary is exercised against real git output.
// evidence.Main validates FORGE_CI_TEST_PROFILE up front and states the
// shortfall once at the end, so a run that proved the boundary and one that
// skipped past it no longer look the same.
//
// isolatedgit.Main isolates the whole process first, so the adapter's own git,
// which inherits os.Environ(), cannot read the developer's configuration.
func TestMain(m *testing.M) {
	os.Exit(isolatedgit.Main(func() int { return evidence.Main(m) }))
}
