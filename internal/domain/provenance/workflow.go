// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package provenance

import (
	"strings"
	"unicode"

	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// ValidWorkflowPath reports whether workflow can name a workflow file inside
// a repository: a relative path with no parent-directory step and no control
// character. A signer records only such a value, and a verifier expects only
// such a value, so the two sides of a lineage check hold one rule.
func ValidWorkflowPath(workflow string) bool {
	return pathsafe.Relative(workflow) && !strings.ContainsFunc(workflow, unicode.IsControl)
}
