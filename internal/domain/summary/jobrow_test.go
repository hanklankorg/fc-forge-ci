// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package summary_test

import (
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/summary"
)

func TestParseJobRows(t *testing.T) {
	t.Parallel()

	rows, err := summary.ParseJobRows([]string{"Check: lint=success", "Build: a=b=failed", "Sign and publish=", "Publish=bogus"})
	if err != nil {
		t.Fatal(err)
	}

	want := []summary.JobRow{
		{Label: "Check: lint", Result: summary.ResultSuccess},
		{Label: "Build: a=b", Result: summary.ResultFailure},
		{Label: "Sign and publish", Result: summary.ResultFailure},
		{Label: "Publish", Result: summary.ResultFailure},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}

	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %v, want %v", i, rows[i], want[i])
		}
	}

	for _, bad := range []string{"success", "=success", " =skipped"} {
		if _, err := summary.ParseJobRows([]string{bad}); !errors.Is(err, errs.ErrMalformedInput) {
			t.Errorf("ParseJobRows(%q) error = %v, want ErrMalformedInput", bad, err)
		}
	}
}
