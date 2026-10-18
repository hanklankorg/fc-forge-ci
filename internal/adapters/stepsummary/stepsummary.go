// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package stepsummary implements ci.SummarySink as an append-only writer for a
// configured per-step markdown summary path.
//
// With an empty path, New is a no-op; NewWithLog writes to its supplied log
// fallback. Publishing GitLab summary files as artifacts belongs to the workflow.
package stepsummary

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
)

// Sink writes append-only markdown to a step-summary file. When no file
// is configured it either no-ops or, if a log fallback is set, echoes the
// markdown to that writer — used for runners (Forgejo/Gitea) that don't
// expose a summary file, so the content lands in the job log instead of
// being silently dropped (go-gitea/gitea#27898, nektos/act#1187).
type Sink struct {
	path string
	log  io.Writer // fallback when path == "" (nil → silent no-op)
	// section also writes every summary to log inside a GitLab log section,
	// even when path is set: GitLab renders no summary file, so the job log
	// is where a reader sees it, and the file stays a job artifact.
	section  bool
	sections int
	mu       sync.Mutex
}

// New returns a Sink writing to the given path. Empty path → no-op.
func New(path string) *Sink { return &Sink{path: path} }

// NewWithLog returns a Sink that writes to path when set, and otherwise
// echoes the markdown to log (the job log) rather than dropping it. Used
// for the Forgejo runner, whose summary file may be absent.
func NewWithLog(path string, log io.Writer) *Sink { return &Sink{path: path, log: log} }

// NewGitLab returns a Sink writing to path when set and always echoing each
// summary to log as its own GitLab log section.
func NewGitLab(path string, log io.Writer) *Sink { return &Sink{path: path, log: log, section: true} }

// Append implements ci.SummarySink.
func (s *Sink) Append(_ context.Context, markdown string) error {
	if s == nil {
		return nil
	}

	if s.section {
		if err := s.appendSection(markdown); err != nil {
			return err
		}

		if s.path == "" {
			return nil
		}
	}

	if s.path == "" {
		return s.appendToLog(markdown)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := cliio.OpenAppendNoFollow(s.path, 0o644) //nolint:varnamelen // step-summary file read by GitHub Actions runner.
	if err != nil {
		return fmt.Errorf("open step summary %q: %w", s.path, err)
	}

	// Close is checked too: a failed write can surface only when the file closes.
	_, writeErr := f.WriteString(markdown)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return fmt.Errorf("write step summary: %w", err)
	}

	return nil
}

// appendToLog echoes the summary markdown to the fallback writer (no-op
// when unset). Serialised so concurrent appends don't interleave.
func (s *Sink) appendToLog(markdown string) error {
	if s.log == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := io.WriteString(s.log, markdown); err != nil {
		return fmt.Errorf("write step summary to log: %w", err)
	}

	return nil
}

// appendSection writes markdown to log between GitLab's section markers, so
// the job log shows it under a "Summary" heading that a reader can fold.
func (s *Sink) appendSection(markdown string) error {
	if s.log == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.sections++
	name := fmt.Sprintf("forge_ci_summary_%d", s.sections)
	now := time.Now().Unix()

	if _, err := fmt.Fprintf(s.log, "\x1b[0Ksection_start:%d:%s\r\x1b[0KSummary\n%s\n\x1b[0Ksection_end:%d:%s\r\x1b[0K\n",
		now, name, strings.TrimRight(markdown, "\n"), now, name); err != nil {
		return fmt.Errorf("write step summary section: %w", err)
	}

	return nil
}

// Compile-time conformance check.
var _ ci.SummarySink = (*Sink)(nil)
