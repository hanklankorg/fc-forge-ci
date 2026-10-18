// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package summary

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// JobRow is one row of a summary's status table, labelled as the forge shows
// the job. Forges without GitHub's stage graph, GitLab and Forgejo, name their
// own jobs instead of filling GitHub's fixed target rows.
type JobRow struct {
	Label  string
	Result Result
}

// ParseJobRows parses "Label=result" pairs. A job label may contain "=", so
// the result is what follows the last one. The result is read as the job's own
// status, fail-closed: an unknown value renders as a failure.
func ParseJobRows(pairs []string) ([]JobRow, error) {
	rows := make([]JobRow, 0, len(pairs))

	for _, pair := range pairs {
		cut := strings.LastIndex(pair, "=")
		if cut <= 0 || strings.TrimSpace(pair[:cut]) == "" {
			return nil, fmt.Errorf("job %q is not \"Label=result\": %w", pair, errs.ErrMalformedInput)
		}

		rows = append(rows, JobRow{Label: strings.TrimSpace(pair[:cut]), Result: NormalizeJobStatus(strings.TrimSpace(pair[cut+1:]))})
	}

	return rows, nil
}
