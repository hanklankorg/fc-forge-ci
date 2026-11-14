// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package changelog_test

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

func TestMain(m *testing.M) {
	os.Exit(evidence.Main(m))
}
