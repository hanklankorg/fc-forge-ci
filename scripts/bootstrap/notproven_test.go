// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package bootstrap

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

// The installer tests that unpack a real archive skip without tar, unzip or a
// SHA-256 tool. evidence.Main validates FORGE_CI_TEST_PROFILE before the
// first test and names the gap once at the end, rather than leaving it in a
// skip line the tier's "ok" hides.
func TestMain(m *testing.M) {
	os.Exit(evidence.Main(m))
}
