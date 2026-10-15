// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

func TestMain(m *testing.M) {
	os.Exit(livetest.RunSuite(m.Run))
}
