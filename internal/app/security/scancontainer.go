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
	"strings"

	"github.com/diggsweden/forge-ci/internal/clicolor"
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
	// Severity is the trivy --severity filter and the gate's threshold.
	// Default: "CRITICAL,HIGH". A finding at this severity or above blocks
	// when a fixed package exists for it, and an unfixed CRITICAL blocks
	// unless the ignore file approves it; other unfixed findings are
	// reported without blocking. To skip the gate entirely, set the
	// workflow's `enable-scan: false`; narrowing the filter (e.g.
	// "CRITICAL") relaxes the threshold without disabling the scan.
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
//  2. Parse JSON, count findings at/above Severity → fail when non-zero
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
//
// TRIVY_PLATFORM (when set in the env) is inherited by the trivy
// subprocess automatically; the multi-arch container build pins per-leg
// platforms that way.
//
//nolint:cyclop // validate the severity vocabulary before the scan/report phases.
func ScanContainer(
	ctx context.Context,
	trivy TrivyOps,
	out, stderr io.Writer,
	annot output.Annotator,
	in ScanContainerInput,
) error {
	if in.ImageRef == "" {
		return fmt.Errorf("image-ref is required: %w", errs.ErrUsage)
	}

	jsonPath := cmp.Or(in.JSONFile, security.DefaultTrivyContainerJSONFile)
	sarifPath := cmp.Or(in.SARIFFile, security.DefaultTrivyContainerSARIFFile)
	gitlabPath := cmp.Or(in.GitLabReportFile, security.DefaultTrivyGitLabContainerFile)
	severity := cmp.Or(in.Severity, "CRITICAL,HIGH")

	parts := strings.Split(severity, ",")
	for index, part := range parts {
		part = strings.ToUpper(strings.TrimSpace(part))
		switch part {
		case "UNKNOWN", "LOW", "MEDIUM", "HIGH", "CRITICAL":
			parts[index] = part
		default:
			return fmt.Errorf("unsupported container scan severity %q: %w", part, errs.ErrUsage)
		}
	}

	severity = strings.Join(parts, ",")

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
	_, _ = fmt.Fprintf(out, "   Severity filter: %s\n", severity)
	_, _ = fmt.Fprintf(out, "   Approved findings: %s\n\n", cmp.Or(ignoreFile, "none (no .trivyignore.yaml)"))

	if scanErr := runTrivyContainerScan(ctx, trivy, out, stderr, rawReport, severity, ignoreFile, in.ImageRef, in.OCILayout); scanErr != nil {
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
		_, _ = fmt.Fprintf(out, "%s No vulnerabilities found at severity %s or above\n", clicolor.Check(out), severity)

		return nil
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

	blocking, reported := gateFindings(findings)
	printFindings(out, blocking, reported, ignoreFile)

	if len(reported) != 0 {
		annot.Warningf("%d container %s at severity %s or above %s no fixed package yet; reported, not blocking",
			len(reported), plural(len(reported), "vulnerability", "vulnerabilities"), severity, plural(len(reported), "has", "have"))
	}

	if len(blocking) == 0 {
		_, _ = fmt.Fprintf(out, "%s No blocking vulnerabilities at severity %s or above\n", clicolor.Check(out), severity)

		return nil
	}

	msg := fmt.Sprintf("Found %d blocking container %s at severity %s or above", len(blocking),
		plural(len(blocking), "vulnerability", "vulnerabilities"), severity)
	annot.Errorf("%s", msg)
	// Domain rule failure (scan threshold exceeded) — wrap so the CLI
	// exits with ExitCodeValidation (1), not ExitCodeSoftware (70).
	return fmt.Errorf("%s: %w", msg, errs.ErrValidation)
}

// gateFindings splits the findings the gate blocks on from those it only
// reports: a finding with a fixed package blocks, and so does an unfixed
// CRITICAL, which the ignore file can approve; other unfixed findings are
// reported. Nothing the consumer can act on passes silently, and nothing it
// cannot act on stops a release unless it is critical.
func gateFindings(findings security.TrivyFindings) ([]security.TrivyFinding, []security.TrivyFinding) {
	blocking := slices.Clone(findings.Fixable)

	var reported []security.TrivyFinding

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
// reported, one finding per line.
func printFindings(out io.Writer, blocking, reported []security.TrivyFinding, ignoreFile string) {
	approve := cmp.Or(ignoreFile, defaultIgnoreFile)

	if len(blocking) != 0 {
		_, _ = fmt.Fprintf(out, "%s Blocking: %d\n", clicolor.Cross(out), len(blocking))

		for _, finding := range blocking {
			reason := "fixed in " + finding.Fixed
			if finding.Fixed == "" {
				reason = "no fixed package yet; approve it in " + approve + " with a reason and expiry"
			}

			_, _ = fmt.Fprintf(out, "   %-8s  %-20s  %s %s  (%s)\n", finding.Severity, finding.ID, finding.Package, finding.Installed, reason)
		}

		_, _ = fmt.Fprintln(out)
	}

	if len(reported) != 0 {
		_, _ = fmt.Fprintf(out, "Not blocking, no fixed package yet: %d\n", len(reported))

		for index, finding := range reported {
			if index == maxReportedLines {
				_, _ = fmt.Fprintf(out, "   ... and %d more in the scan reports\n", len(reported)-maxReportedLines)

				break
			}

			_, _ = fmt.Fprintf(out, "   %-8s  %-20s  %s %s\n", finding.Severity, finding.ID, finding.Package, finding.Installed)
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
// `--exit-code 0` keeps the scan-gate decision inside the Go code.
func runTrivyContainerScan(ctx context.Context, trivy TrivyOps, out, stderr io.Writer, jsonPath, severity, ignoreFile, imageRef, ociLayout string) error {
	args := []string{
		"image",
		"--format", "json",
		"--output", jsonPath,
		"--severity", severity,
		"--vuln-type", "os,library",
		"--exit-code", "0",
	}
	if ignoreFile != "" {
		args = append(args, "--ignorefile", ignoreFile)
	}

	if ociLayout != "" {
		args = append(args, "--input", ociLayout)
	} else {
		args = append(args, imageRef)
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
