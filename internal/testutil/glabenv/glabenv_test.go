// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package glabenv_test

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/glabenv"
)

// TestSetup_DoesNotAlsoClaimAnotherForge keeps the two runner fixtures
// mutually exclusive.
//
// Forge detection asks each marker in turn, so a fixture that leaves a second
// forge's marker set makes the answer depend on the order the detector happens
// to check in. That is not hypothetical here: the two fixtures are used in the
// same packages, and an inherited GITHUB_ACTIONS from the developer's own shell
// or from a previous helper is exactly the kind of ambient state these
// fixtures exist to control.
func TestSetup_DoesNotAlsoClaimAnotherForge(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	glabenv.Setup(t)

	if got := os.Getenv("GITHUB_ACTIONS"); got == "true" {
		t.Errorf("the GitLab fixture left GITHUB_ACTIONS=%q set; detection depends on which marker is read first", got)
	}
}
