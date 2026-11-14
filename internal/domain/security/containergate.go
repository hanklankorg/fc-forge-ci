// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// DefaultContainerBlockSeverity is the container scan gate's default
// threshold: a CRITICAL finding with a fixed package blocks, and HIGH
// findings are reported without blocking.
const DefaultContainerBlockSeverity = "CRITICAL"

// containerReportFloor is the least severe level a container scan always
// reports, whatever its threshold, so relaxing the gate never hides it.
const containerReportFloor = "HIGH"

// ContainerSeverityGate is a container scan's severity gate: the threshold a
// finding with a fixed package blocks at, and the levels the scan reports.
type ContainerSeverityGate struct {
	threshold string
}

// ParseContainerSeverityGate reads a gate threshold: one severity, or a
// comma-list whose least severe entry is the threshold, so "CRITICAL,HIGH"
// blocks at HIGH and above. Empty is the default, CRITICAL. Case and spaces
// around entries are ignored; an unknown or empty entry is a usage error.
func ParseContainerSeverityGate(value string) (ContainerSeverityGate, error) {
	if value == "" {
		return ContainerSeverityGate{}, nil
	}

	threshold := ""

	for part := range strings.SplitSeq(value, ",") {
		level := strings.ToUpper(strings.TrimSpace(part))
		if !slices.Contains(severityOrder, level) {
			return ContainerSeverityGate{}, fmt.Errorf("unsupported container scan severity %q: %w", level, errs.ErrUsage)
		}

		if threshold == "" || rankOf(level) > rankOf(threshold) {
			threshold = level
		}
	}

	return ContainerSeverityGate{threshold: threshold}, nil
}

// Threshold is the least severe level that blocks; the zero gate has the
// default.
func (g ContainerSeverityGate) Threshold() string {
	return cmp.Or(g.threshold, DefaultContainerBlockSeverity)
}

// Blocks reports whether a finding at severity, with a fixed package, is at
// or above the threshold.
func (g ContainerSeverityGate) Blocks(severity string) bool {
	return rankOf(severity) <= rankOf(g.Threshold())
}

// ReportSeverities is the Trivy --severity filter: every level from CRITICAL
// down to HIGH, or down to the threshold when it is lower.
func (g ContainerSeverityGate) ReportSeverities() string {
	floor := max(rankOf(g.Threshold()), rankOf(containerReportFloor))

	return strings.Join(severityOrder[:floor+1], ",")
}
