// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security_test

import (
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/security"
)

// The setting is the blocking threshold only: a comma-list blocks at its
// least severe entry, and the scan reports down to HIGH whatever it blocks on.
func TestParseContainerSeverityGate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value, threshold, report string
		blocks, passes           []string
	}{
		{"", "CRITICAL", "CRITICAL,HIGH", []string{"CRITICAL"}, []string{"HIGH", "MEDIUM"}},
		{"CRITICAL", "CRITICAL", "CRITICAL,HIGH", []string{"CRITICAL"}, []string{"HIGH"}},
		{"CRITICAL,HIGH", "HIGH", "CRITICAL,HIGH", []string{"CRITICAL", "HIGH"}, []string{"MEDIUM"}},
		{"high", "HIGH", "CRITICAL,HIGH", []string{"CRITICAL", "HIGH"}, []string{"MEDIUM"}},
		{" MEDIUM , critical", "MEDIUM", "CRITICAL,HIGH,MEDIUM", []string{"HIGH", "MEDIUM"}, []string{"LOW"}},
		{"UNKNOWN", "UNKNOWN", "CRITICAL,HIGH,MEDIUM,LOW,UNKNOWN", []string{"LOW", "UNKNOWN"}, nil},
	} {
		gate, err := security.ParseContainerSeverityGate(tc.value)
		if err != nil {
			t.Fatalf("%q: %v", tc.value, err)
		}

		if gate.Threshold() != tc.threshold || gate.ReportSeverities() != tc.report {
			t.Errorf("%q: threshold %s, report %s; want %s, %s", tc.value, gate.Threshold(), gate.ReportSeverities(), tc.threshold, tc.report)
		}

		for _, severity := range tc.blocks {
			if !gate.Blocks(severity) {
				t.Errorf("%q: %s does not block", tc.value, severity)
			}
		}

		for _, severity := range tc.passes {
			if gate.Blocks(severity) {
				t.Errorf("%q: %s blocks", tc.value, severity)
			}
		}
	}

	for _, value := range []string{"SEVERE", "HIGH,", ","} {
		if _, err := security.ParseContainerSeverityGate(value); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("%q: err = %v, want ErrUsage", value, err)
		}
	}
}
