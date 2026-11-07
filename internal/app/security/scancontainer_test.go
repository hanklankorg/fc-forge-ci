// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	appsecurity "github.com/diggsweden/forge-ci/internal/app/security"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
)

// Threshold/blocking tests opt into enforcement rather than relying on the
// shipped default; operational-failure cases keep testing report-only mode.
func enforcingVulnerabilities(ctx context.Context) context.Context {
	return appenforcement.WithAdmin(ctx, enforcement.Admin{enforcement.Vulnerabilities: "true"})
}

// recordingTrivy captures invocations and optionally pre-stages output files.
type recordingTrivy struct {
	runs      [][]string
	runErr    error
	writeJSON []byte // non-nil writes this report at the requested private output path
}

func (r *recordingTrivy) RunInherit(_ context.Context, _, _ io.Writer, args ...string) (int, error) {
	r.runs = append(r.runs, append([]string{}, args...))
	if r.writeJSON != nil {
		writeFixtureReport(scanOutput(args), string(r.writeJSON), args)
	}

	return 0, r.runErr
}

// writeFixtureReport writes a fake tool's report and panics if it cannot.
//
// The fakes used to return a write failure as the tool's own error, so a broken
// fixture reached the product as "trivy failed" -- and a test asserting how the
// product handles a failing scan could pass because the fixture broke. A panic
// fails the test at once, naming the fixture rather than the code under test,
// without threading a *testing.T through every struct literal. A report
// requested for a call that names no output path is the same kind of fixture
// mistake and fails the same way.
func writeFixtureReport(path, body string, args []string) {
	if path == "" {
		panic(fmt.Sprintf("fake tool: a report was scripted for a call with no output path: %q", args))
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		panic(fmt.Sprintf("fake tool: cannot write the scripted report to %s: %v", path, err))
	}
}

// TestFakes_FailLoudlyOnFixtureMistakes pins the fake's own contract, since
// every container-scan test depends on it. A scripted report that cannot be
// written, or is scripted for a call with no output path, must stop the test
// rather than reach the product as a tool failure.
func TestFakes_FailLoudlyOnFixtureMistakes(t *testing.T) {
	t.Parallel()

	mustPanic := func(t *testing.T, name string, fn func()) {
		t.Helper()

		defer func() {
			if recover() == nil {
				t.Errorf("%s: the fake carried on instead of failing the test", name)
			}
		}()

		fn()
	}

	unwritable := filepath.Join(t.TempDir(), "missing-dir", "report.json")

	mustPanic(t, "unwritable report", func() {
		_, _ = (&recordingTrivy{writeJSON: []byte("{}")}).RunInherit(context.Background(), io.Discard, io.Discard, "--output", unwritable)
	})

	mustPanic(t, "report scripted without an output path", func() {
		_, _ = (&recordingTrivy{writeJSON: []byte("{}")}).RunInherit(context.Background(), io.Discard, io.Discard, "image", "x")
	})

	// An unscripted call is the deliberate no-write case.
	if code, err := (&recordingTrivy{}).RunInherit(context.Background(), io.Discard, io.Discard, "--output", unwritable); code != 0 || err != nil {
		t.Errorf("an unscripted call = (%d, %v), want a silent success", code, err)
	}
}

// TestScanContainer_FailsWhenFindingsAtThreshold is the deployable-pipeline
// guarantee: if trivy reports any vulnerability at the configured severity
// or above, the workflow fails with ErrValidation. SARIF + GitLab reports
// are still derived so Code Scanning gets the finding detail.
func TestScanContainer_FailsWhenFindingsAtThreshold(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "trivy.json")
	sarifPath := filepath.Join(dir, "trivy.sarif")
	glPath := filepath.Join(dir, "gl.json")

	body := []byte(`{
        "ArtifactName": "ghcr.io/example/img",
        "Results": [{
            "Target": "alpine",
            "Vulnerabilities": [{
                "VulnerabilityID": "CVE-2025-0001",
                "PkgName": "openssl",
                "InstalledVersion": "3.0.0",
                "FixedVersion": "3.0.1",
                "Severity": "HIGH",
                "Title": "OpenSSL bug"
            }]
        }]
    }`)

	trivy := &recordingTrivy{writeJSON: body}

	var log bytes.Buffer

	err := appsecurity.ScanContainer(enforcingVulnerabilities(t.Context()), trivy, &log, &bytes.Buffer{}, output.Annotator{}, appsecurity.ScanContainerInput{
		ImageRef:         "ghcr.io/example/img@sha256:deadbeef",
		JSONFile:         jsonPath,
		SARIFFile:        sarifPath,
		GitLabReportFile: glPath,
		Severity:         "CRITICAL,HIGH",
	})
	if err == nil {
		t.Fatal("expected error when HIGH finding at threshold, got nil")
	}

	if !errors.Is(err, errs.ErrValidation) {
		t.Errorf("err = %v, want wrapped ErrValidation", err)
	}

	if !strings.Contains(err.Error(), "1 blocking container vulnerability") {
		t.Errorf("err message should mention finding count: %v", err)
	}

	for _, want := range []string{`Affected target: "alpine"`, "fixed in 3.0.1", "Scan report: " + jsonPath, "rebuild, and rerun the scan", "reason and expiry"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("scan log missing remediation %q: %s", want, log.String())
		}
	}

	// SARIF + GitLab reports must be present so the failure is visible
	// in Code Scanning + GitLab. The pipeline rejects the artifact, but
	// the rejection comes with full triage detail attached.
	if _, statErr := os.Stat(sarifPath); statErr != nil {
		t.Errorf("SARIF should be written even when gate fails: %v", statErr)
	}

	if _, statErr := os.Stat(glPath); statErr != nil {
		t.Errorf("GitLab report should be written even when gate fails: %v", statErr)
	}
}

func TestScanContainer_IdentifiesEmbeddedGoRuntime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	var log bytes.Buffer

	trivy := &recordingTrivy{writeJSON: []byte(`{"Results":[{
		"Target":"usr/bin/pebble","Type":"gobinary","Vulnerabilities":[{
			"VulnerabilityID":"CVE-2026-78667","PkgName":"stdlib",
			"InstalledVersion":"v1.27.1","FixedVersion":"1.27.2","Severity":"CRITICAL"
		}]}]}`)}

	err := appsecurity.ScanContainer(enforcingVulnerabilities(t.Context()), trivy, &log, io.Discard, output.Annotator{}, appsecurity.ScanContainerInput{
		ImageRef:         "registry.example/java@sha256:fixture",
		JSONFile:         filepath.Join(dir, "trivy.json"),
		SARIFFile:        filepath.Join(dir, "trivy.sarif"),
		GitLabReportFile: filepath.Join(dir, "gitlab.json"),
	})
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("expected blocking finding, got %v", err)
	}

	for _, want := range []string{`Affected target: "usr/bin/pebble"`, "fixed in 1.27.2", "rebuild that binary", "Updating the scanner does not fix the binary"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("missing %q in scan guidance: %s", want, log.String())
		}
	}
}

// Empty bytes are missing scan evidence, not a clean JSON report.
func TestScanContainer_RejectsEmptyJSON(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "trivy.json")

	trivy := &recordingTrivy{writeJSON: []byte{}}
	if err := appsecurity.ScanContainer(context.Background(), trivy, &bytes.Buffer{}, &bytes.Buffer{}, output.Annotator{}, appsecurity.ScanContainerInput{
		ImageRef:         "img",
		JSONFile:         jsonPath,
		SARIFFile:        filepath.Join(dir, "trivy.sarif"),
		GitLabReportFile: filepath.Join(dir, "gl.json"),
	}); !errors.Is(err, errs.ErrMalformedInput) {
		t.Fatalf("expected malformed-input refusal on empty JSON, got: %v", err)
	}

	if len(trivy.runs) != 1 {
		t.Errorf("expected only the image scan when JSON is empty, got %d invocations", len(trivy.runs))
	}
}

// TestScanContainer_PassesWhenJSONHasNoVulns: trivy emitted JSON with
// ArtifactName / Results metadata but no actual vulnerabilities (e.g.
// the per-arch trivy output for a clean image). Pipeline passes.
func TestScanContainer_PassesWhenJSONHasNoVulns(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "trivy.json")
	sarifPath := filepath.Join(dir, "trivy.sarif")
	glPath := filepath.Join(dir, "gl.json")

	body := []byte(`{"ArtifactName": "ghcr.io/example/img", "Results": [{"Target": "alpine", "Vulnerabilities": []}]}`)
	trivy := &recordingTrivy{writeJSON: body}

	if err := appsecurity.ScanContainer(context.Background(), trivy, &bytes.Buffer{}, &bytes.Buffer{}, output.Annotator{}, appsecurity.ScanContainerInput{
		ImageRef:         "ghcr.io/example/img@sha256:deadbeef",
		JSONFile:         jsonPath,
		SARIFFile:        sarifPath,
		GitLabReportFile: glPath,
		Severity:         "CRITICAL,HIGH",
	}); err != nil {
		t.Fatalf("expected success on zero-vuln JSON, got: %v", err)
	}
}

func TestScanContainer_RejectsMissingImageRef(t *testing.T) {
	trivy := &recordingTrivy{}

	err := appsecurity.ScanContainer(context.Background(), trivy, io.Discard, io.Discard, output.Annotator{}, appsecurity.ScanContainerInput{})
	// A missing --image-ref is a broken invocation, not a finding: ErrUsage
	// exits 2 rather than 1, which would read as "the image is vulnerable".
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}

	if !strings.Contains(err.Error(), "image-ref is required") {
		t.Errorf("err = %v, want it to name the missing flag", err)
	}

	if len(trivy.runs) != 0 {
		t.Errorf("scanned without an image ref: %v", trivy.runs)
	}
}

func TestScanContainer_PropagatesScanFailure(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "trivy.json")
	boom := errors.New("trivy boom")
	trivy := &recordingTrivy{runErr: boom}

	err := appsecurity.ScanContainer(context.Background(), trivy, &bytes.Buffer{}, &bytes.Buffer{}, output.Annotator{}, appsecurity.ScanContainerInput{
		ImageRef:         "img",
		JSONFile:         jsonPath,
		SARIFFile:        filepath.Join(dir, "report.sarif"),
		GitLabReportFile: filepath.Join(dir, "gitlab.json"),
	})
	// The cause has to survive the wrap, or an operator debugging a broken
	// scanner sees only "trivy image" with no reason attached.
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}

	if !strings.Contains(err.Error(), "trivy image") {
		t.Errorf("err = %v, want it to name the failing step", err)
	}
}

// TestScanContainer_ImageScanArgsCarryThresholdAndExitZero pins the
// trivy invocation shape: a CRITICAL threshold still reports HIGH,
// --exit-code 0 so the gate decision stays in-process, and the image ref
// carried verbatim.
func TestScanContainer_ImageScanArgsCarryThresholdAndExitZero(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "trivy.json")
	trivy := &recordingTrivy{writeJSON: []byte(`{"Results":[]}`)}

	if err := appsecurity.ScanContainer(context.Background(), trivy, &bytes.Buffer{}, &bytes.Buffer{}, output.Annotator{}, appsecurity.ScanContainerInput{
		ImageRef:         "ghcr.io/example/img@sha256:deadbeef",
		JSONFile:         jsonPath,
		Severity:         "CRITICAL",
		SARIFFile:        filepath.Join(dir, "report.sarif"),
		GitLabReportFile: filepath.Join(dir, "gitlab.json"),
	}); err != nil {
		t.Fatal(err)
	}

	if len(trivy.runs) != 1 {
		t.Fatalf("trivy invocations = %d, want exactly the image scan", len(trivy.runs))
	}

	privateOutput := scanOutput(trivy.runs[0])
	if privateOutput == jsonPath || !filepath.IsAbs(privateOutput) {
		t.Fatalf("scan did not use a private absolute report: %q", privateOutput)
	}

	// The whole invocation, not a membership check per flag: a set test
	// cannot tell "--severity CRITICAL,HIGH" from "--severity" followed by
	// some other value with CRITICAL,HIGH sitting elsewhere in the line.
	want := []string{
		"image",
		"--format", "json",
		"--output", privateOutput,
		"--severity", "CRITICAL,HIGH",
		"--pkg-types", "os,library",
		"--exit-code", "0",
		"ghcr.io/example/img@sha256:deadbeef",
	}
	if !slices.Equal(trivy.runs[0], want) {
		t.Errorf("image args =\n%q\nwant\n%q", trivy.runs[0], want)
	}
}

// unfixedTrivyJSON has one finding per severity with no fixed package.
const unfixedTrivyJSON = `{"ArtifactName":"img","Results":[{"Target":"debian","Vulnerabilities":[
{"VulnerabilityID":"CVE-C","PkgName":"libc6","InstalledVersion":"2.41","Severity":"CRITICAL"},
{"VulnerabilityID":"CVE-H","PkgName":"git","InstalledVersion":"2.47","Severity":"HIGH"}]}]}`

func TestScanContainer_ReceiptCountsObservedAndEligible(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	for _, tc := range []struct {
		name            string
		body            string
		total, eligible int
	}{
		{"clean", `{"Results":[]}`, 0, 0},
		{"nonblocking", strings.Replace(unfixedTrivyJSON, `"CRITICAL"`, `"HIGH"`, 1), 2, 0},
		{"mixed", unfixedTrivyJSON, 2, 1},
		{"blocking", strings.Replace(fixableTrivyJSON, `"HIGH"`, `"CRITICAL"`, 1), 2, 2},
	} {
		for _, enforced := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/enforce=%t", tc.name, enforced), func(t *testing.T) {
				dir := t.TempDir()
				in := appsecurity.ScanContainerInput{
					ImageRef: "img", Severity: "CRITICAL",
					JSONFile:         filepath.Join(dir, "raw.json"),
					SARIFFile:        filepath.Join(dir, "report.sarif"),
					GitLabReportFile: filepath.Join(dir, "gitlab.json"),
				}
				ctx := appenforcement.WithAdmin(t.Context(), enforcement.Admin{enforcement.Vulnerabilities: fmt.Sprint(enforced)})
				var out bytes.Buffer
				err := appsecurity.ScanContainer(ctx, &recordingTrivy{writeJSON: []byte(tc.body)}, &out, io.Discard, output.Annotator{}, in)
				if enforced && tc.eligible > 0 {
					if !errors.Is(err, errs.ErrValidation) {
						t.Fatalf("want validation failure, got %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				result := "findings"
				if tc.total == 0 {
					result = "clean"
				}
				want := fmt.Sprintf("vulnerabilities result=%s count=%d enforcement=%t source=flag blocking-count=%d\n", result, tc.total, enforced, tc.eligible)
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing receipt %q:\n%s", want, out.String())
				}
				raw, err := os.ReadFile(in.JSONFile)
				if err != nil || string(raw) != tc.body {
					t.Fatalf("raw report changed: %s, %v", raw, err)
				}
				if tc.total > 0 {
					for _, path := range []string{in.SARIFFile, in.GitLabReportFile} {
						body, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						for _, id := range []string{"CVE-C", "CVE-H"} {
							if !bytes.Contains(body, []byte(id)) {
								t.Errorf("%s lost observed finding %s: %s", path, id, body)
							}
						}
						// The receipt must not filter or otherwise change the raw-derived reports.
						expectedPath := path + ".expected"
						transform := appsecurity.TransformInput{InputPath: in.JSONFile, OutputPath: expectedPath, ImageRef: in.ImageRef}
						if path == in.SARIFFile {
							err = appsecurity.TrivyToSARIF(transform)
						} else {
							_, err = appsecurity.TrivyToGitLabContainer(transform)
						}
						if err != nil {
							t.Fatal(err)
						}
						expected, err := os.ReadFile(expectedPath)
						if err != nil || !bytes.Equal(body, expected) {
							t.Fatalf("%s differs from the raw-derived report: %v", path, err)
						}
					}
				}
			})
		}
	}
}

// The consumer cannot fix an unfixed finding, so only a critical one stops
// the release, until the ignore file approves it; an unfixed HIGH is
// reported and the gate passes.
func TestScanContainer_UnfixedFindingsBlockOnlyWhenCritical(t *testing.T) {
	for name, tc := range map[string]struct {
		body        string
		wantBlocked bool
	}{
		"unfixed critical blocks":   {body: unfixedTrivyJSON, wantBlocked: true},
		"unfixed high is reported":  {body: strings.Replace(unfixedTrivyJSON, `"CRITICAL"`, `"HIGH"`, 1)},
		"approved critical is gone": {body: `{"Results":[{"Target":"debian","Vulnerabilities":[{"VulnerabilityID":"CVE-H","PkgName":"git","InstalledVersion":"2.47","Severity":"HIGH"}]}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			var out bytes.Buffer

			err := appsecurity.ScanContainer(enforcingVulnerabilities(t.Context()), &recordingTrivy{writeJSON: []byte(tc.body)}, &out, io.Discard, output.Annotator{}, appsecurity.ScanContainerInput{
				ImageRef:         "img",
				JSONFile:         filepath.Join(dir, "trivy.json"),
				SARIFFile:        filepath.Join(dir, "trivy.sarif"),
				GitLabReportFile: filepath.Join(dir, "gl.json"),
				Severity:         "CRITICAL,HIGH",
			})
			if blocked := errors.Is(err, errs.ErrValidation); blocked != tc.wantBlocked {
				t.Fatalf("err = %v, want blocked=%v\n%s", err, tc.wantBlocked, out.String())
			}

			if !strings.Contains(out.String(), "CVE-H") || !strings.Contains(out.String(), "Reported, not blocking") {
				t.Errorf("unfixed HIGH not reported:\n%s", out.String())
			}

			if tc.wantBlocked && !strings.Contains(out.String(), "approve it in .trivyignore.yaml") {
				t.Errorf("blocking unfixed critical gives no way forward:\n%s", out.String())
			}
		})
	}
}

// fixableTrivyJSON has one finding per severity with a fixed package.
const fixableTrivyJSON = `{"ArtifactName":"img","Results":[{"Target":"debian","Vulnerabilities":[
{"VulnerabilityID":"CVE-C","PkgName":"libc6","InstalledVersion":"2.41","FixedVersion":"2.42","Severity":"CRITICAL"},
{"VulnerabilityID":"CVE-H","PkgName":"git","InstalledVersion":"2.47","FixedVersion":"2.48","Severity":"HIGH"}]}]}`

// The threshold decides which fixable findings block; the default blocks only
// on CRITICAL and still reports HIGH, and a comma-list blocks at its least
// severe entry, so an existing "CRITICAL,HIGH" keeps blocking on HIGH.
func TestScanContainer_ThresholdDecidesWhichFixableFindingsBlock(t *testing.T) {
	highOnly := strings.Replace(fixableTrivyJSON, `"CRITICAL"`, `"HIGH"`, 1)

	for name, tc := range map[string]struct {
		severity     string
		body         string
		wantBlocking []string
		wantReported []string
	}{
		"default blocks a fixable critical":   {body: fixableTrivyJSON, wantBlocking: []string{"CVE-C"}, wantReported: []string{"CVE-H"}},
		"default reports a fixable high":      {body: highOnly, wantReported: []string{"CVE-C", "CVE-H"}},
		"CRITICAL,HIGH blocks a fixable high": {severity: "CRITICAL,HIGH", body: highOnly, wantBlocking: []string{"CVE-C", "CVE-H"}},
		"HIGH blocks a fixable high":          {severity: "high", body: fixableTrivyJSON, wantBlocking: []string{"CVE-C", "CVE-H"}},
		"default blocks an unfixed critical":  {body: unfixedTrivyJSON, wantBlocking: []string{"CVE-C"}, wantReported: []string{"CVE-H"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			var out bytes.Buffer

			trivy := &recordingTrivy{writeJSON: []byte(tc.body)}

			err := appsecurity.ScanContainer(enforcingVulnerabilities(t.Context()), trivy, &out, io.Discard, output.Annotator{}, appsecurity.ScanContainerInput{
				ImageRef:         "img",
				JSONFile:         filepath.Join(dir, "trivy.json"),
				SARIFFile:        filepath.Join(dir, "trivy.sarif"),
				GitLabReportFile: filepath.Join(dir, "gl.json"),
				Severity:         tc.severity,
			})
			if blocked := errors.Is(err, errs.ErrValidation); blocked != (len(tc.wantBlocking) != 0) {
				t.Fatalf("err = %v, want blocking %v\n%s", err, tc.wantBlocking, out.String())
			}

			if got := reportFlag(trivy.runs[0], "--severity"); got != "CRITICAL,HIGH" {
				t.Errorf("--severity %q, want CRITICAL,HIGH: HIGH is reported at every threshold down to HIGH", got)
			}

			blocking, reported, _ := strings.Cut(out.String(), "Reported, not blocking")
			for _, id := range tc.wantBlocking {
				if !strings.Contains(blocking, id) {
					t.Errorf("%s not listed as blocking:\n%s", id, out.String())
				}
			}

			for _, id := range tc.wantReported {
				if !strings.Contains(reported, id) {
					t.Errorf("%s not listed as reported, not blocking:\n%s", id, out.String())
				}
			}
		})
	}
}

// A named ignore file reaches Trivy, which drops the findings it approves;
// a named file that does not exist is a broken invocation.
func TestScanContainer_IgnoreFile(t *testing.T) {
	dir := t.TempDir()
	ignore := filepath.Join(dir, "approved.yaml")
	in := appsecurity.ScanContainerInput{
		ImageRef:         "img",
		JSONFile:         filepath.Join(dir, "trivy.json"),
		SARIFFile:        filepath.Join(dir, "trivy.sarif"),
		GitLabReportFile: filepath.Join(dir, "gl.json"),
		IgnoreFile:       ignore,
	}

	if err := appsecurity.ScanContainer(t.Context(), &recordingTrivy{}, io.Discard, io.Discard, output.Annotator{}, in); !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("missing ignore file: err = %v, want ErrUsage", err)
	}

	if err := os.WriteFile(ignore, []byte("vulnerabilities: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	trivy := &recordingTrivy{writeJSON: []byte(`{"Results":[]}`)}
	if err := appsecurity.ScanContainer(t.Context(), trivy, io.Discard, io.Discard, output.Annotator{}, in); err != nil {
		t.Fatal(err)
	}

	if args := trivy.runs[0]; !slices.Contains(args, "--ignorefile") || args[slices.Index(args, "--ignorefile")+1] != ignore {
		t.Errorf("image args = %q, want --ignorefile %s", args, ignore)
	}
}

// Trivy runs in a private directory, so the repository's own ignore file
// and a relative layout reach it as absolute paths.
func TestScanContainer_PassesPathsAbsolute(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, ".trivyignore.yaml"), []byte("vulnerabilities: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	trivy := &recordingTrivy{writeJSON: []byte(`{"Results":[]}`)}
	if err := appsecurity.ScanContainer(t.Context(), trivy, io.Discard, io.Discard, output.Annotator{}, appsecurity.ScanContainerInput{
		ImageRef:         "img",
		OCILayout:        "layout",
		JSONFile:         "trivy.json",
		SARIFFile:        "trivy.sarif",
		GitLabReportFile: "gl.json",
	}); err != nil {
		t.Fatal(err)
	}

	for flag, want := range map[string]string{"--ignorefile": filepath.Join(dir, ".trivyignore.yaml"), "--input": filepath.Join(dir, "layout")} {
		if got := reportFlag(trivy.runs[0], flag); got != want {
			t.Errorf("%s %q, want %q", flag, got, want)
		}
	}
}

// The platform of a multi-platform input is a flag of the scan, so it
// reaches Trivy although no TRIVY_* variable does; without one, Trivy is
// given none.
func TestScanContainer_PassesThePlatformAsAFlag(t *testing.T) {
	t.Chdir(t.TempDir())

	for platform, want := range map[string]bool{"linux/arm64": true, "": false} {
		trivy := &recordingTrivy{writeJSON: []byte(`{"Results":[]}`)}
		if err := appsecurity.ScanContainer(t.Context(), trivy, io.Discard, io.Discard, output.Annotator{}, appsecurity.ScanContainerInput{
			ImageRef: "img", OCILayout: "layout", Platform: platform,
			JSONFile: "trivy.json", SARIFFile: "trivy.sarif", GitLabReportFile: "gl.json",
		}); err != nil {
			t.Fatal(err)
		}

		if got := slices.Contains(trivy.runs[0], "--platform"); got != want {
			t.Errorf("platform %q: args %q", platform, trivy.runs[0])
		}

		if want && reportFlag(trivy.runs[0], "--platform") != platform {
			t.Errorf("--platform %q, want %q", reportFlag(trivy.runs[0], "--platform"), platform)
		}
	}
}

// A plain .trivyignore records no reason or expiry; without the YAML file
// the scan refuses to run rather than block on what it meant to approve.
func TestScanContainer_RefusesAPlainTrivyignore(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, ".trivyignore"), []byte("CVE-2026-0001\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	trivy := &recordingTrivy{}

	err := appsecurity.ScanContainer(t.Context(), trivy, io.Discard, io.Discard, output.Annotator{}, appsecurity.ScanContainerInput{
		ImageRef:         "img",
		JSONFile:         filepath.Join(dir, "trivy.json"),
		SARIFFile:        filepath.Join(dir, "trivy.sarif"),
		GitLabReportFile: filepath.Join(dir, "gl.json"),
	})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "convert it to .trivyignore.yaml") {
		t.Fatalf("err = %v, want a conversion request", err)
	}

	if len(trivy.runs) != 0 {
		t.Errorf("scanned with an unconverted ignore file: %v", trivy.runs)
	}
}
