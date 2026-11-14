// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement_test

import (
	"strings"
	"testing"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
)

// The summary names what blocks and what only reports, and marks a setting
// the command line changed.
func TestSummarySaysWhatBlocks(t *testing.T) {
	t.Parallel()

	policy, err := enforcement.Policy{}.WithAdmin(enforcement.Admin{enforcement.Lint: "true"})
	if err != nil {
		t.Fatal(err)
	}

	got := appenforcement.Summary(policy)
	for _, want := range []string{"blocks lint (flag)", "reports without blocking vulnerabilities", "admin-policy"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary = %q, lacks %q", got, want)
		}
	}

	if got := appenforcement.Summary(enforcement.Policy{}); !strings.HasPrefix(got, "blocks nothing;") {
		t.Errorf("Summary of an all-false policy = %q", got)
	}
}
