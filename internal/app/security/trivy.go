// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package security wires `forge-ci security <subcmd>` to the domain
// transforms. Each subcommand is one file.
package security

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/security"
)

// reportTimestamp returns the timestamp baked into security-report
// metadata. SOURCE_DATE_EPOCH (seconds since UNIX epoch,
// reproducible-builds.org convention) wins when set so two runs over
// the same trivy input produce byte-identical reports; otherwise
// time.Now().UTC() is used. A value that is set but unusable is refused, as
// resolveBuildDate in app/build/go.go refuses it: falling back to the wall
// clock would quietly produce a report that is not reproducible.
func reportTimestamp() (time.Time, error) {
	if raw := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH")); raw != "" {
		return build.ParseSourceDateEpoch(raw)
	}

	return time.Now().UTC(), nil
}

// TransformInput drives the report transforms.
type TransformInput struct {
	InputPath    string
	OutputPath   string
	TrivyVersion string
	ImageRef     string // Trivy transforms only; ignored by the SAST transform
}

// TrivyToGitLabContainer loads the Trivy JSON at InputPath, transforms
// it to the container-scanning shape (location.image / operating_system),
// and writes the result.
func TrivyToGitLabContainer(in TransformInput) (int, error) {
	if err := validateReportPaths(in.InputPath, in.OutputPath); err != nil {
		return 0, err
	}

	report, err := loadTrivy(in.InputPath)
	if err != nil {
		return 0, err
	}

	now, err := reportTimestamp()
	if err != nil {
		return 0, err
	}

	gl := security.TrivyToGitLabContainer(report, security.Options{
		TrivyVersion: in.TrivyVersion,
		ImageRef:     in.ImageRef,
		Now:          now,
	})
	if err := writeJSON(in.OutputPath, gl); err != nil {
		return 0, err
	}

	return len(gl.Vulnerabilities), nil
}

// TrivyToSARIF loads the Trivy JSON at InputPath, transforms it via the
// in-process SARIF builder, and writes the SARIF v2.1.0 document to
// OutputPath. Replaces `trivy convert --format sarif` — eliminates one
// subprocess per container scan and removes trivy as a runtime
// dependency for the conversion step.
func TrivyToSARIF(in TransformInput) error {
	if err := validateReportPaths(in.InputPath, in.OutputPath); err != nil {
		return err
	}

	report, err := loadTrivy(in.InputPath)
	if err != nil {
		return err
	}

	now, err := reportTimestamp()
	if err != nil {
		return err
	}

	doc := security.TrivyToSARIF(report, security.Options{
		TrivyVersion: in.TrivyVersion,
		ImageRef:     in.ImageRef,
		Now:          now,
	})

	// Render before the destination is opened, as writeJSON marshals first: a
	// rendering failure must not truncate the previous report to nothing.
	var body bytes.Buffer
	if renderErr := doc.PrettyWrite(&body); renderErr != nil {
		return fmt.Errorf("render SARIF: %w", renderErr)
	}

	if err := cliio.WriteFile(in.OutputPath, body.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write SARIF %q: %w", in.OutputPath, err)
	}

	return nil
}

func loadTrivy(path string) (*security.TrivyReport, error) {
	data, err := cliio.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read trivy report %q: %w: %w", path, err, errs.ErrMissingInput)
		}

		return nil, fmt.Errorf("read trivy report %q: %w", path, err)
	}

	return security.ParseTrivyReport(data)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil { //nolint:gosec // GitLab security report read by runner.
		return fmt.Errorf("write %q: %w", path, err)
	}

	return nil
}
