// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appsecurity "github.com/diggsweden/forge-ci/internal/app/security"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	domainsecurity "github.com/diggsweden/forge-ci/internal/domain/security"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

type scanCall struct {
	args        []string
	out, stderr io.Writer
}
type scanRecorder struct {
	calls []scanCall
	run   func(context.Context, []string) (int, error)
}

func (f *scanRecorder) RunInherit(ctx context.Context, out, stderr io.Writer, args ...string) (int, error) {
	f.calls = append(f.calls, scanCall{args: slices.Clone(args), out: out, stderr: stderr})

	return f.run(ctx, args)
}
func scanOutput(args []string) string {
	for i, arg := range args {
		if arg == "--output" && i+1 < len(args) {
			return args[i+1]
		}
	}

	return ""
}

const boundaryTrivyJSON = `{"ArtifactName":"fixture-image","Results":[{"Target":"first","Vulnerabilities":[{"VulnerabilityID":"CVE-A","PkgName":"alpha","InstalledVersion":"1.0","FixedVersion":"1.1","Severity":"HIGH","Title":"Alpha issue"}]},{"Target":"second","Vulnerabilities":[{"VulnerabilityID":"CVE-B","PkgName":"beta","InstalledVersion":"2.0","FixedVersion":"2.1","Severity":"CRITICAL","Title":"Beta issue"}]}]}`

func TestContainerReportBoundary_CompleteFindingAssociations(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)
	in := appsecurity.ScanContainerInput{ImageRef: "registry.example/owner/image:tag", JSONFile: fsys.Path("raw.json"), SARIFFile: fsys.Path("report.sarif"), GitLabReportFile: fsys.Path("report.json"), TrivyVersion: "fixture-version"}
	scanner := &scanRecorder{run: func(_ context.Context, args []string) (int, error) {
		return 0, os.WriteFile(scanOutput(args), []byte(boundaryTrivyJSON), 0o600)
	}}

	err := appsecurity.ScanContainer(enforcingVulnerabilities(t.Context()), scanner, io.Discard, io.Discard, output.Annotator{}, in)
	// With enforcement enabled, the severity gate blocks on the CRITICAL only; the HIGH it reports
	// without blocking still reaches both reports below.
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "1 blocking container vulnerability") {
		t.Fatalf("gate=%v", err)
	}

	var sarif struct {
		Runs []struct {
			Results []struct {
				RuleID  string `json:"ruleId"`
				Level   string `json:"level"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					Physical struct {
						Artifact struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(fsys.ReadFile("report.sarif"), &sarif); err != nil {
		t.Fatal(err)
	}

	if len(sarif.Runs) != 1 || len(sarif.Runs[0].Results) != 2 {
		t.Fatalf("SARIF lost findings: %+v", sarif)
	}

	for i, want := range []struct{ id, message string }{{"CVE-A", "Alpha issue \u2014 alpha@1.0 (fix: 1.1)"}, {"CVE-B", "Beta issue \u2014 beta@2.0 (fix: 2.1)"}} {
		got := sarif.Runs[0].Results[i]
		if got.RuleID != want.id || got.Message.Text != want.message || got.Level != "error" || len(got.Locations) != 1 || got.Locations[0].Physical.Artifact.URI != in.ImageRef {
			t.Fatalf("SARIF association=%+v", got)
		}
	}

	var gitlab domainsecurity.GitLabReport
	if err := json.Unmarshal(fsys.ReadFile("report.json"), &gitlab); err != nil {
		t.Fatal(err)
	}

	if len(gitlab.Vulnerabilities) != 2 {
		t.Fatalf("GitLab lost findings: %+v", gitlab)
	}

	for i, want := range []struct{ id, pkg, version, severity string }{{"CVE-A", "alpha", "1.0", "High"}, {"CVE-B", "beta", "2.0", "Critical"}} {
		got := gitlab.Vulnerabilities[i]
		if len(got.Identifiers) == 0 || got.Identifiers[0].Value != want.id || got.Location.Dependency == nil || got.Location.Dependency.Package.Name != want.pkg || got.Location.Dependency.Version != want.version || got.Severity != want.severity || got.Location.Image != in.ImageRef {
			t.Fatalf("GitLab association=%+v", got)
		}
	}
}

func TestContainerReportBoundary_StatusShapeAndAliases(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"status", "offline", "null", "malformed", "alias", "hardlink"} {
		fsys := testfs.NewReal(t)
		raw := fsys.WriteFile("raw.json", []byte("prior raw"))

		report := fsys.Path("report.sarif")
		if mode == "alias" {
			report = fsys.Path("./raw.json")
		}

		if mode == "hardlink" {
			if err := os.Link(raw, report); err != nil {
				t.Fatal(err)
			}
		}

		var scanner *scanRecorder

		scanner = &scanRecorder{run: func(_ context.Context, args []string) (int, error) {
			body := `{"Results":[]}`
			if mode == "null" {
				body = "null"
			}

			if mode == "malformed" {
				body = "{"
			}

			if err := os.WriteFile(scanOutput(args), []byte(body), 0o600); err != nil {
				return 0, err
			}

			if mode == "status" {
				return 7, nil
			}

			if mode == "offline" {
				_, _ = io.WriteString(scanner.calls[0].stderr, "FATAL\tinit error: Get \"https://mirror.example/v2/\": dial tcp: lookup mirror.example: no such host\n")

				return 1, nil
			}

			return 0, nil
		}}

		var out bytes.Buffer

		err := appsecurity.ScanContainer(t.Context(), scanner, &out, &out, output.Annotator{}, appsecurity.ScanContainerInput{ImageRef: "image", JSONFile: raw, SARIFFile: report, GitLabReportFile: fsys.Path("gitlab.json")})
		if err == nil {
			t.Fatalf("mode=%s passed", mode)
		}

		// A scan Trivy fails is Trivy's failure; only one the network
		// stopped is an unavailable dependency.
		if mode == "status" && (!errors.Is(err, errs.ErrValidation) || errors.Is(err, errs.ErrDependencyUnavailable)) {
			t.Fatalf("status err=%v", err)
		}

		if mode == "offline" && !errors.Is(err, errs.ErrDependencyUnavailable) {
			t.Fatalf("offline err=%v", err)
		}

		if mode == "null" || mode == "malformed" {
			if !errors.Is(err, errs.ErrMalformedInput) {
				t.Fatalf("shape err=%v", err)
			}
		}

		if mode == "alias" || mode == "hardlink" {
			if len(scanner.calls) != 0 || out.Len() != 0 || string(fsys.ReadFile("raw.json")) != "prior raw" {
				t.Fatalf("alias refusal had effects: %v", err)
			}
		}
	}
}

// TestContainerReportBoundary_OneFailedReportLeavesItsSibling makes one
// derived report impossible to install, in each direction. The derived
// reports are what the scan step hands on, so the scan fails and names the
// lost report whether findings are enforced or only reported, where a scan
// with findings would otherwise exit 0 with no evidence. The derivations stay
// independent: the other report still reaches its destination with every
// finding, the raw report is published, and the gate's verdict is kept.
func TestContainerReportBoundary_OneFailedReportLeavesItsSibling(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("permission bits do not restrict root")
	}

	for _, blocked := range []string{"sarif", "gitlab", "sarif report-only", "gitlab report-only"} {
		t.Run(blocked, func(t *testing.T) {
			t.Parallel()

			blocked, reportOnly := strings.CutSuffix(blocked, " report-only")

			fsys := testfs.NewReal(t)
			readOnly := fsys.MkdirAll("read-only")

			in := appsecurity.ScanContainerInput{
				ImageRef: "registry.example/owner/image:tag", JSONFile: fsys.Path("raw.json"),
				SARIFFile: fsys.Path("report.sarif"), GitLabReportFile: fsys.Path("report.json"),
			}
			if blocked == "sarif" {
				in.SARIFFile = filepath.Join(readOnly, "report.sarif")
			} else {
				in.GitLabReportFile = filepath.Join(readOnly, "report.json")
			}

			if err := os.Chmod(readOnly, 0o500); err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

			scanner := &scanRecorder{run: func(_ context.Context, args []string) (int, error) {
				return 0, os.WriteFile(scanOutput(args), []byte(boundaryTrivyJSON), 0o600)
			}}

			var diagnostics bytes.Buffer

			ctx := enforcingVulnerabilities(t.Context())
			if reportOnly {
				ctx = t.Context()
			}

			err := appsecurity.ScanContainer(ctx, scanner, io.Discard, io.Discard, output.NewAnnotator(&diagnostics, output.FormatGitHub), in)
			if err == nil || !strings.Contains(err.Error(), filepath.Join(readOnly, "report.")) {
				t.Fatalf("err = %v, want a failure naming the lost report", err)
			}

			if blocks := errors.Is(err, errs.ErrValidation) && strings.Contains(err.Error(), "Found 1 blocking container vulnerability"); blocks == reportOnly {
				t.Errorf("err = %v, want the gate's verdict beside the lost report", err)
			}

			if !json.Valid(fsys.ReadFile("raw.json")) {
				t.Error("the raw report was not published")
			}

			assertSiblingReport(t, fsys, blocked, diagnostics.String())

			if entries, readErr := os.ReadDir(readOnly); readErr != nil || len(entries) != 0 {
				t.Errorf("blocked directory holds %v (err %v), want nothing", entries, readErr)
			}
		})
	}
}

// A clean scan is a written verdict too: when one of its reports cannot be
// written the scan fails and names it, and the other report is still there.
func TestContainerReportBoundary_CleanScanFailsOnAnUnwrittenReport(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("permission bits do not restrict root")
	}

	fsys := testfs.NewReal(t)
	readOnly := fsys.MkdirAll("read-only")

	in := appsecurity.ScanContainerInput{
		ImageRef: "registry.example/owner/image:tag", JSONFile: fsys.Path("raw.json"),
		SARIFFile: filepath.Join(readOnly, "report.sarif"), GitLabReportFile: fsys.Path("report.json"),
	}

	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	scanner := &scanRecorder{run: func(_ context.Context, args []string) (int, error) {
		return 0, os.WriteFile(scanOutput(args), []byte(`{"Results":[]}`), 0o600)
	}}

	var out bytes.Buffer

	err := appsecurity.ScanContainer(t.Context(), scanner, &out, io.Discard, output.Annotator{}, in)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(readOnly, "report.sarif")) {
		t.Fatalf("err = %v, want a failure naming the lost report", err)
	}

	if !strings.Contains(out.String(), "No vulnerabilities found") || !json.Valid(fsys.ReadFile("report.json")) {
		t.Errorf("the clean verdict or its other report was lost:\n%s", out.String())
	}
}

// assertSiblingReport checks the report that was not blocked carries both
// findings and the blocked one's failure was annotated.
func assertSiblingReport(t *testing.T, fsys *testfs.Real, blocked, diagnostics string) {
	t.Helper()

	if blocked == "sarif" {
		if !strings.Contains(diagnostics, "::error::write the SARIF scan report") {
			t.Errorf("diagnostics = %q, want the SARIF failure annotated", diagnostics)
		}

		var gitlab domainsecurity.GitLabReport
		if err := json.Unmarshal(fsys.ReadFile("report.json"), &gitlab); err != nil || len(gitlab.Vulnerabilities) != 2 {
			t.Errorf("GitLab report = %+v (err %v), want both findings", gitlab, err)
		}

		return
	}

	if !strings.Contains(diagnostics, "::error::write the GitLab container-scanning report") {
		t.Errorf("diagnostics = %q, want the GitLab failure annotated", diagnostics)
	}

	var sarif struct {
		Runs []struct {
			Results []json.RawMessage `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(fsys.ReadFile("report.sarif"), &sarif); err != nil || len(sarif.Runs) != 1 || len(sarif.Runs[0].Results) != 2 {
		t.Errorf("SARIF = %+v (err %v), want both findings", sarif, err)
	}
}
