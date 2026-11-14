// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

// TestMain isolates the whole process for the scratch repositories these
// tests build, so the git the code under test runs, which inherits
// os.Environ(), cannot read the developer's configuration either.
func TestMain(m *testing.M) {
	os.Exit(isolatedgit.Main(m.Run))
}
