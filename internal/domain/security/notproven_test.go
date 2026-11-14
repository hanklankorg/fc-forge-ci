// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security_test

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

// The native Trivy envelope is the only evidence that real Trivy output
// survives ParseTrivyReport, and it skips without the binary. evidence.Main
// validates FORGE_CI_TEST_PROFILE before the first test and names the gap
// once at the end, rather than leaving it in a skip line the tier's "ok"
// hides.
func TestMain(m *testing.M) {
	os.Exit(evidence.Main(m))
}
