// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package security

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/clicolor"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/security"
)

// TrivyOps abstracts the trivy adapter for dependency injection.
type TrivyOps interface {
	RunInherit(ctx context.Context, w, stderr io.Writer, args ...string) (int, error)
}

// ScanContainerInput drives ScanContainer.
type ScanContainerInput struct {
	// ImageRef is the image to scan (registry/name@digest or registry/name:tag).
	// Required.
	ImageRef string
	// OCILayout selects a local image as scan input; ImageRef remains report identity.
	OCILayout string
	// Platform is the os/arch of a multi-platform input to scan, such as
	// linux/arm64. Empty leaves the choice to Trivy.
	Platform string
	// JSONFile is the destination for the raw trivy JSON output.
	// Default: trivy-results.json.
	JSONFile string
	// SARIFFile is the destination for the SARIF report derived in-process.
	// Default: trivy-results.sarif. Skipped when the parsed JSON
	// scan returned zero findings.
	SARIFFile string
	// GitLabReportFile is the destination for the GitLab
	// container-scanning report derived via the in-process transform.
	// Default: gl-container-scanning-report.json.
	GitLabReportFile string
	// TrivyVersion is embedded into the GitLab report metadata.
	TrivyVersion string
	// Severity is the gate's blocking threshold: one level, or a comma-list
	// whose least severe entry is the threshold. Default: "CRITICAL". A
	// finding at this severity or above blocks when a fixed package exists
	// for it, and an unfixed CRITICAL blocks unless the ignore file approves
	// it; every other finding is reported without blocking. The scan always
	// reports down to HIGH, or down to the threshold when it is lower. To
	// skip the gate entirely, set the admin-owned enforcement policy; scan
	// execution and report generation remain mandatory for exported/published
	// layouts.
	Severity string
	// IgnoreFile is the Trivy ignore file that approves findings, with the
	// reason and expiry Trivy's YAML format records. Empty takes the
	// repository's own, .trivyignore.yaml or .trivyignore, when present.
	IgnoreFile string
}

const (
	defaultIgnoreFile = ".trivyignore.yaml"
	plainIgnoreFile   = ".trivyignore"
)

// resolveIgnoreFile returns the ignore file to pass to Trivy, or "" for
// none. A named file must exist; the repository's .trivyignore.yaml is used
// when present. A plain .trivyignore records no reason or expiry, and Trivy
// would apply it on its own, so one without the YAML file is refused, as
// Nanolinter's scans refuse it.
func resolveIgnoreFile(named string) (string, error) {
	if named != "" {
		if _, err := os.Stat(named); err != nil {
			return "", fmt.Errorf("ignore file %s: %w", named, errs.ErrUsage)
		}

		return named, nil
	}

	if info, err := os.Stat(defaultIgnoreFile); err == nil && info.Mode().IsRegular() {
		return defaultIgnoreFile, nil
	}

	if _, err := os.Stat(plainIgnoreFile); err == nil {
		return "", fmt.Errorf("%s found without %s: convert it to %s with statement and expired_at for every entry: %w",
			plainIgnoreFile, defaultIgnoreFile, defaultIgnoreFile, errs.ErrUsage)
	}

	return "", nil
}

// ScanContainer orchestrates a container scan in four steps:
//  1. `trivy image --format json` → private report (trivy does the scan)
//  2. Parse JSON, count blocking findings (see gateFindings) → fail when non-zero
//  3. in-process TrivyToSARIF       → SARIFFile (skipped with no findings)
//  4. in-process TrivyToGitLabContainer → GitLabReportFile
//
// Trivy itself is invoked with `--exit-code 0` so the failure decision
// stays inside the Go code (trivy reports, we gate). SARIF + GitLab transforms still run after a failing gate so
// the Code Scanning tab + GitLab report carry the same findings the
// pipeline rejected on — making the failure visible in the same places
// where adopters look for vulnerability detail.
// Configured output files represent this invocation: previous files are retired
// after preflight; fresh validated reports are installed individually.
func ScanContainer(
	ctx context.Context,
	trivy TrivyOps,
	out, stderr io.Writer,
	annot output.Annotator,
	in ScanContainerInput,
) error {
	if _, err := appenforcement.Resolve(ctx); err != nil {
		return err
	}

	if in.ImageRef == "" {
		return fmt.Errorf("image-ref is required: %w", errs.ErrUsage)
	}

	jsonPath := cmp.Or(in.JSONFile, security.DefaultTrivyContainerJSONFile)
	sarifPath := cmp.Or(in.SARIFFile, security.DefaultTrivyContainerSARIFFile)
	gitlabPath := cmp.Or(in.GitLabReportFile, security.DefaultTrivyGitLabContainerFile)

	gate, err := security.ParseContainerSeverityGate(in.Severity)
	if err != nil {
		return err
	}

	threshold, severity := gate.Threshold(), gate.ReportSeverities()

	ignoreFile, err := resolveIgnoreFile(in.IgnoreFile)
	if err != nil {
		return err
	}

	workDir, err := prepareScanReports(jsonPath, sarifPath, gitlabPath)
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(workDir) }()

	rawReport := filepath.Join(workDir, "raw.json")

	_, _ = fmt.Fprintf(out, "🔍 Container vulnerability scan\n")
	_, _ = fmt.Fprintf(out, "   Image: %s\n", in.ImageRef)
	_, _ = fmt.Fprintf(out, "   Reported severities: %s\n", severity)
	_, _ = fmt.Fprintf(out, "   Blocking: %s or above with a fixed package, and unfixed CRITICAL\n", threshold)
	_, _ = fmt.Fprintf(out, "   Approved findings: %s\n\n", cmp.Or(ignoreFile, "none (no .trivyignore.yaml)"))

	if scanErr := runTrivyContainerScan(ctx, trivy, out, stderr, rawReport, severity, ignoreFile, in); scanErr != nil {
		return scanErr
	}

	findings, err := loadContainerFindings(rawReport)
	if err != nil {
		return err
	}

	if err := publishScanReport(rawReport, jsonPath); err != nil {
		return err
	}
	// Only a parsed report with no findings is clean; empty bytes are not a scan.
	if len(findings.Fixable)+len(findings.Unfixed) == 0 {
		_, _ = fmt.Fprintf(out, "%s No vulnerabilities found at severity %s\n", clicolor.Check(out), severity)

		return appenforcement.FindingsCounts(ctx, out, enforcement.Vulnerabilities, 0, 0)
	}

	// Always derive SARIF + GitLab reports — even when the gate fails,
	// the artifacts must reach Code Scanning so the failure is visible
	// in the same places adopters use for vulnerability triage.
	deriveContainerReports(out, annot, TransformInput{
		InputPath:    rawReport,
		OutputPath:   filepath.Join(workDir, "report.sarif"),
		TrivyVersion: in.TrivyVersion,
		ImageRef:     in.ImageRef,
	}, filepath.Join(workDir, "gitlab.json"), sarifPath, gitlabPath)

	blocking, reported := gateFindings(gate, findings)
	printFindings(out, blocking, reported, threshold, ignoreFile)

	if len(reported) != 0 {
		annot.Warningf("%d container %s below the %s threshold or without a fixed package; reported, not blocking",
			len(reported), plural(len(reported), "vulnerability", "vulnerabilities"), threshold)
	}

	if len(blocking) == 0 {
		_, _ = fmt.Fprintf(out, "%s No blocking vulnerabilities (threshold %s)\n", clicolor.Check(out), threshold)

		return appenforcement.FindingsCounts(ctx, out, enforcement.Vulnerabilities, len(blocking)+len(reported), len(blocking))
	}

	msg := fmt.Sprintf("Found %d blocking container %s (threshold %s)", len(blocking),
		plural(len(blocking), "vulnerability", "vulnerabilities"), threshold)
	_, _ = fmt.Fprintf(out, "Scan report: %s\n", jsonPath)
	_, _ = fmt.Fprintln(out, "Next: update the affected dependency or pinned base image, rebuild, and rerun the scan before publishing or promoting.")
	_, _ = fmt.Fprintf(out, "If a fix cannot be deployed, request a reviewed CVE-specific exception in %s with a reason and expiry; the admin policy decides whether these findings block.\n", cmp.Or(ignoreFile, defaultIgnoreFile))

	if err := appenforcement.FindingsCounts(ctx, out, enforcement.Vulnerabilities, len(blocking)+len(reported), len(blocking)); err != nil {
		annot.Errorf("%s", msg)

		return fmt.Errorf("%s: %w", msg, err)
	}

	annot.Warningf("%s; reported under non-enforcing admin policy", msg)

	return nil
}

// gateFindings splits the findings the gate blocks on from those it only
// reports: a finding with a fixed package blocks at or above the gate's
// threshold, and an unfixed CRITICAL blocks whatever the threshold, until the
// ignore file approves it. Every other finding is reported. Nothing the
// consumer cannot act on stops a release unless it is critical.
func gateFindings(gate security.ContainerSeverityGate, findings security.TrivyFindings) ([]security.TrivyFinding, []security.TrivyFinding) {
	var blocking, reported []security.TrivyFinding

	for _, finding := range findings.Fixable {
		if gate.Blocks(finding.Severity) {
			blocking = append(blocking, finding)
		} else {
			reported = append(reported, finding)
		}
	}

	for _, finding := range findings.Unfixed {
		if finding.Severity == "CRITICAL" {
			blocking = append(blocking, finding)
		} else {
			reported = append(reported, finding)
		}
	}

	return blocking, reported
}

// maxReportedLines bounds the list of non-blocking findings in the log; the
// scan reports carry all of them.
const maxReportedLines = 20

// printFindings lists what blocks the release and why, then what is only
// reported and why not, one finding per line.
func printFindings(out io.Writer, blocking, reported []security.TrivyFinding, threshold, ignoreFile string) {
	approve := cmp.Or(ignoreFile, defaultIgnoreFile)

	if len(blocking) != 0 {
		_, _ = fmt.Fprintf(out, "%s Blocking: %d\n", clicolor.Cross(out), len(blocking))

		for _, finding := range blocking {
			reason := "fixed in " + finding.Fixed
			if finding.Fixed == "" {
				reason = "no fixed package yet; approve it in " + approve + " with a reason and expiry"
			}

			_, _ = fmt.Fprintf(out, "   %-8s  %-20s  %s %s  (%s)\n", finding.Severity, finding.ID, finding.Package, finding.Installed, reason)
			if finding.Target != "" {
				_, _ = fmt.Fprintf(out, "     Affected target: %q\n", finding.Target)
			}
		}

		if slices.ContainsFunc(blocking, func(finding security.TrivyFinding) bool { return finding.Package == "stdlib" }) {
			_, _ = fmt.Fprintln(out, "Go stdlib findings identify an embedded runtime: for each affected target, rebuild that binary with a fixed Go toolchain, or update the base image that supplies it. Updating the scanner does not fix the binary.")
		}

		_, _ = fmt.Fprintln(out)
	}

	if len(reported) != 0 {
		_, _ = fmt.Fprintf(out, "Reported, not blocking: %d\n", len(reported))

		for index, finding := range reported {
			if index == maxReportedLines {
				_, _ = fmt.Fprintf(out, "   ... and %d more in the scan reports\n", len(reported)-maxReportedLines)

				break
			}

			reason := "no fixed package yet"
			if finding.Fixed != "" {
				reason = "fixed in " + finding.Fixed + "; below the " + threshold + " threshold"
			}

			_, _ = fmt.Fprintf(out, "   %-8s  %-20s  %s %s  (%s)\n", finding.Severity, finding.ID, finding.Package, finding.Installed, reason)
		}

		_, _ = fmt.Fprintln(out)
	}
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}

	return many
}

// runTrivyContainerScan invokes `trivy image --format json` against
// the image ref and writes the JSON output to jsonPath.
// `--exit-code 0` keeps the scan-gate decision inside the Go code. Trivy
// runs outside the caller's directory, so every path is made absolute.
func runTrivyContainerScan(ctx context.Context, trivy TrivyOps, out, stderr io.Writer, jsonPath, severity, ignoreFile string, in ScanContainerInput) error {
	paths, err := absPaths(jsonPath, ignoreFile, in.OCILayout)
	if err != nil {
		return err
	}

	jsonPath, ignoreFile, ociLayout := paths[0], paths[1], paths[2]

	args := []string{
		"image",
		"--format", "json",
		"--output", jsonPath,
		"--severity", severity,
		"--pkg-types", "os,library",
		"--exit-code", "0",
	}
	if ignoreFile != "" {
		args = append(args, "--ignorefile", ignoreFile)
	}

	if in.Platform != "" {
		args = append(args, "--platform", in.Platform)
	}

	if ociLayout != "" {
		args = append(args, "--input", ociLayout)
	} else {
		args = append(args, in.ImageRef)
	}

	code, err := trivy.RunInherit(ctx, out, stderr, args...)
	if err != nil {
		return fmt.Errorf("trivy image: %w", err)
	}

	if code != 0 {
		return fmt.Errorf("trivy image exited with status %d: %w", code, errs.ErrDependencyUnavailable)
	}

	return nil
}

// absPaths returns each non-empty path made absolute.
func absPaths(paths ...string) ([]string, error) {
	out := make([]string, len(paths))

	for index, path := range paths {
		if path == "" {
			continue
		}

		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", path, err)
		}

		out[index] = absolute
	}

	return out, nil
}

// loadContainerFindings reads a fresh, bounded, regular Trivy report.
func loadContainerFindings(jsonPath string) (security.TrivyFindings, error) {
	body, err := readJSONScanReport(jsonPath)
	if err != nil {
		return security.TrivyFindings{}, fmt.Errorf("read %s: %w", jsonPath, err)
	}

	findings, err := security.ClassifyTrivyFindings(body)
	if err != nil {
		return security.TrivyFindings{}, fmt.Errorf("classify container findings: %w", err)
	}

	return findings, nil
}

// deriveContainerReports derives the SARIF + GitLab container-scanning
// reports from the trivy JSON. Both transforms are best-effort: if
// either fails, the gate still fires below but the artifacts may be
// incomplete. annot.Warningf surfaces the failure.
func deriveContainerReports(out io.Writer, annot output.Annotator, in TransformInput, gitlabPath, sarifDestination, gitlabDestination string) {
	_, _ = fmt.Fprintln(out, "Converting JSON to SARIF...")

	if err := TrivyToSARIF(in); err != nil {
		annot.Warningf("convert to SARIF failed: %v", err)
	} else if err := publishScanReport(in.OutputPath, sarifDestination); err != nil {
		annot.Warningf("publish SARIF failed: %v", err)
	}

	_, _ = fmt.Fprintln(out, "Generating GitLab container-scanning report...")

	gitlabIn := in
	gitlabIn.OutputPath = gitlabPath

	if _, err := TrivyToGitLabContainer(gitlabIn); err != nil {
		annot.Warningf("TrivyToGitLabContainer: %v", err)
	} else if err := publishScanReport(gitlabPath, gitlabDestination); err != nil {
		annot.Warningf("publish GitLab container report failed: %v", err)
	}
}
