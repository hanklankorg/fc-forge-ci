// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

func TestDigestMarkerRestrictiveUmask(t *testing.T) {
	evidence.UnderUmask(t, "^TestWriteDigestMarker_")
}
