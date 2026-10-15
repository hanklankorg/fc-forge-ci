// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah_test

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

// The integration tier here skips when buildah is absent, which is the whole
// of the container evidence this package can produce. evidence.Main checks
// FORGE_CI_TEST_PROFILE before the first test and, at the end, names what
// the run did not prove -- otherwise the skip scrolls past and the package
// still reports "ok".
//
// No build tag: the untagged tiers must go through the same accounting, and a
// TestMain that exists only under //go:build integration would leave the
// profile unvalidated for every other run of this package.
func TestMain(m *testing.M) {
	os.Exit(evidence.Main(m))
}
