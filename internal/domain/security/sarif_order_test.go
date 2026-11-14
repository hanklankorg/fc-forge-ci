// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security_test

import (
	"encoding/json"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/security"
	"github.com/stretchr/testify/require"
)

func TestTrivySARIF_EquivalentOrderHasIdenticalBytes(t *testing.T) {
	t.Parallel()

	alpha := security.TrivyVulnerability{VulnerabilityID: "CVE-fixture", Title: "same", Description: "alpha", Severity: "HIGH", PkgName: "pkg", InstalledVersion: "1", PrimaryURL: "https://a.invalid"}
	beta := alpha
	beta.Description = "beta"
	beta.Severity = "LOW"
	beta.PrimaryURL = "https://b.invalid"
	first := &security.TrivyReport{Results: []security.TrivyResult{{Vulnerabilities: []security.TrivyVulnerability{alpha, beta}}}}
	second := &security.TrivyReport{Results: []security.TrivyResult{{Vulnerabilities: []security.TrivyVulnerability{beta, alpha}}}}
	left, err := json.Marshal(security.TrivyToSARIF(first, security.Options{}))
	require.NoError(t, err)
	right, err := json.Marshal(security.TrivyToSARIF(second, security.Options{}))
	require.NoError(t, err)
	require.Equal(t, string(left), string(right))
	require.Equal(t, beta, second.Results[0].Vulnerabilities[0])
}
