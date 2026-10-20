// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/security"
	"github.com/stretchr/testify/require"
)

func TestParseTrivyReport_RejectsObjectsWithoutReportEvidence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, body string }{
		{"empty-object", `{}`},
		{"unknown-only", `{"unexpected":true}`},
		{"metadata-only", `{"Metadata":{"OS":{"Family":"alpine"}}}`},
		{"unnamed-null-results", `{"Results":null}`},
		{"empty-name", `{"ArtifactName":""}`},
		{"blank-name", `{"ArtifactName":" \t\r\n"}`},
		{"blank-name-null-results", `{"ArtifactName":" \t","Results":null}`},
		{"null-name", `{"ArtifactName":null}`},
		{"null-report", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := security.ParseTrivyReport([]byte(tc.body))
			require.ErrorIs(t, err, errs.ErrMalformedInput)
			require.Nil(t, report)
		})
	}
}

func TestParseTrivyReport_AcceptsSupportedCleanSubsets(t *testing.T) {
	t.Parallel()
	// Synthetic consumed-field subsets, not captured native-version fixtures.
	for _, tc := range []struct {
		name, body string
		want       security.TrivyReport
	}{
		{"empty-array", `{"Results":[]}`, security.TrivyReport{Results: []security.TrivyResult{}}},
		{"blank-name-empty-array", `{"ArtifactName":" \t","Results":[]}`, security.TrivyReport{ArtifactName: " \t", Results: []security.TrivyResult{}}},
		{"unknown-fields", `{"Results":[],"Future":{"nested":[true,1]}}`, security.TrivyReport{Results: []security.TrivyResult{}}},
		{"named-omitted", `{"ArtifactName":"image"}`, security.TrivyReport{ArtifactName: "image"}},
		{"named-null", `{"ArtifactName":"image","Results":null}`, security.TrivyReport{ArtifactName: "image"}},
		{"name-preserved", `{"ArtifactName":" image \t","Future":true}`, security.TrivyReport{ArtifactName: " image \t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := security.ParseTrivyReport([]byte(tc.body))
			require.NoError(t, err)
			require.Equal(t, &tc.want, report)
		})
	}
}

func TestParseTrivyReport_RetainsJSONDecodeCauses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, body string
		syntax     bool
	}{
		{"empty", ``, true},
		{"truncated", `{"Results":[`, true},
		{"trailing-document", `{"Results":[]} {}`, true},
		{"array", `[]`, false},
		{"string", `"report"`, false},
		{"number", `42`, false},
		{"boolean", `true`, false},
		{"artifact-name", `{"ArtifactName":42,"Results":[]}`, false},
		{"results-object-with-name", `{"ArtifactName":"image","Results":{}}`, false},
		{"result-element", `{"Results":[42]}`, false},
		{"target", `{"Results":[{"Target":42}]}`, false},
		{"metadata", `{"ArtifactName":"image","Metadata":[]}`, false},
		{"os-name", `{"Results":[],"Metadata":{"OS":{"Name":42}}}`, false},
		{"vulnerabilities", `{"Results":[{"Vulnerabilities":{}}]}`, false},
		{"severity", `{"Results":[{"Vulnerabilities":[{"Severity":42}]}]}`, false},
		{"references", `{"Results":[{"Vulnerabilities":[{"References":[42]}]}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := security.ParseTrivyReport([]byte(tc.body))
			require.ErrorIs(t, err, errs.ErrMalformedInput)
			require.Nil(t, report)

			if tc.syntax {
				var cause *json.SyntaxError
				require.ErrorAs(t, err, &cause)
			} else {
				var cause *json.UnmarshalTypeError
				require.ErrorAs(t, err, &cause)
			}
		})
	}
}

func TestParseTrivyReport_PreservesConsumedFieldsWithUnknownFields(t *testing.T) {
	t.Parallel()

	report, err := security.ParseTrivyReport([]byte(`{
		"ArtifactName":"image", "SchemaVersion":2, "ArtifactType":"container_image",
		"Metadata":{"OS":{"Family":"alpine","Name":"3.21","Future":true},"Future":{}},
		"Results":[{"Target":"target","Class":"os-pkgs","Future":[],"Vulnerabilities":[{
			"VulnerabilityID":"CVE-2026-1234","PkgName":"pkg","InstalledVersion":"1","FixedVersion":"2",
			"Title":"title","Description":"description","Severity":"HIGH",
			"PrimaryURL":"https://example.invalid/advisory","References":["https://example.invalid/ref"],"Future":true
		}]}], "Future":true
	}`))
	require.NoError(t, err)
	require.Equal(t, &security.TrivyReport{
		ArtifactName: "image",
		Metadata:     &security.TrivyMetadata{OS: &security.TrivyOS{Family: "alpine", Name: "3.21"}},
		Results: []security.TrivyResult{{Target: "target", Class: "os-pkgs", Vulnerabilities: []security.TrivyVulnerability{{
			VulnerabilityID: "CVE-2026-1234", PkgName: "pkg", InstalledVersion: "1", FixedVersion: "2",
			Title: "title", Description: "description", Severity: "HIGH",
			PrimaryURL: "https://example.invalid/advisory", References: []string{"https://example.invalid/ref"},
		}}}},
	}, report)
}

// A finding with a fixed version is fixable, one without is not: the gate
// blocks on the first only. The same vulnerability of the same package,
// reported by two results, is one finding, and the most severe come first.
func TestClassifyTrivyFindings_SplitsByFixedVersion(t *testing.T) {
	t.Parallel()

	body := []byte(`{
  "Results": [
    {"Vulnerabilities": [
      {"VulnerabilityID":"CVE-2024-2","PkgName":"libexpat1","InstalledVersion":"2.8.3","Severity":"HIGH"},
      {"VulnerabilityID":"CVE-2024-1","PkgName":"libssl3t64","InstalledVersion":"3.5.7-u2","FixedVersion":"3.5.7-u3","Severity":"HIGH"},
      {"VulnerabilityID":"CVE-2024-3","PkgName":"zlib1g","InstalledVersion":"1.3","FixedVersion":"1.3.1","Severity":"CRITICAL"}
    ]},
    {"Vulnerabilities": [
      {"VulnerabilityID":"CVE-2024-1","PkgName":"libssl3t64","InstalledVersion":"3.5.7-u2","FixedVersion":"3.5.7-u3","Severity":"HIGH"},
      {"VulnerabilityID":""}
    ]}
  ]
}`)

	got, err := security.ClassifyTrivyFindings(body)
	if err != nil {
		t.Fatal(err)
	}

	want := security.TrivyFindings{
		Fixable: []security.TrivyFinding{
			{ID: "CVE-2024-3", Package: "zlib1g", Installed: "1.3", Fixed: "1.3.1", Severity: "CRITICAL"},
			{ID: "CVE-2024-1", Package: "libssl3t64", Installed: "3.5.7-u2", Fixed: "3.5.7-u3", Severity: "HIGH"},
		},
		Unfixed: []security.TrivyFinding{
			{ID: "CVE-2024-2", Package: "libexpat1", Installed: "2.8.3", Severity: "HIGH"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %+v, want %+v", got, want)
	}
}

func TestClassifyTrivyFindings_EmptyBody(t *testing.T) {
	t.Parallel()

	got, err := security.ClassifyTrivyFindings(nil)
	if err != nil || len(got.Fixable)+len(got.Unfixed) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestClassifyTrivyFindings_PreservesAffectedBinaries(t *testing.T) {
	t.Parallel()

	got, err := security.ClassifyTrivyFindings([]byte(`{"Results":[
		{"Target":"usr/bin/pebble","Vulnerabilities":[
			{"VulnerabilityID":"CVE-2026-78667","PkgName":"stdlib","InstalledVersion":"v1.27.1","FixedVersion":"1.27.2","Severity":"HIGH"},
			{"VulnerabilityID":"CVE-2026-78667","PkgName":"stdlib","InstalledVersion":"v1.27.1","FixedVersion":"1.27.2","Severity":"HIGH"}]},
		{"Target":"usr/bin/another-helper","Vulnerabilities":[
			{"VulnerabilityID":"CVE-2026-78667","PkgName":"stdlib","InstalledVersion":"v1.27.1","FixedVersion":"1.27.2","Severity":"HIGH"}]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Fixable) != 2 {
		t.Fatalf("want one finding per affected binary, got %+v", got)
	}
	if got.Fixable[0].Target != "usr/bin/another-helper" || got.Fixable[1].Target != "usr/bin/pebble" {
		t.Fatalf("affected binaries lost or unsorted: %+v", got.Fixable)
	}
}

func TestClassifyTrivyFindings_IdentifiesNestedPackage(t *testing.T) {
	t.Parallel()
	got, err := security.ClassifyTrivyFindings([]byte(`{"Results":[{
		"Target":"Java","Vulnerabilities":[{
			"VulnerabilityID":"CVE-JAR","PkgName":"jackson-core",
			"PkgPath":"app/application.jar/BOOT-INF/lib/jackson-core-3.1.5.jar",
			"InstalledVersion":"3.1.5","FixedVersion":"3.1.7","Severity":"HIGH"
		}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Fixable) != 1 || got.Fixable[0].Target != "app/application.jar/BOOT-INF/lib/jackson-core-3.1.5.jar" {
		t.Fatalf("want actionable nested package path rather than generic Java target, got %+v", got)
	}
}
