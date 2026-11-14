// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// ValidateContainerfileInput drives ValidateContainerfile.
type ValidateContainerfileInput struct {
	// Path is the requested containerfile path. When it exists,
	// it's used verbatim; otherwise the directory containing it is
	// searched for Dockerfile* / Containerfile* candidates.
	Path string
	// Context opts into source-relative, confined build paths in the current
	// directory. Nil preserves the standalone path resolver's behavior.
	Context *string
	// SourceDirs are further directories the job writes below the current
	// directory; each must be a relative path without a `..` segment.
	SourceDirs []string
}

// ValidateContainerfile resolves a Containerfile path. When Path is an
// existing file, returns it as-is. Otherwise scans Path's directory
// for Dockerfile* / Containerfile* names (non-recursive) and:
//   - errors if no match,
//   - errors if multiple matches (asks for an explicit path),
//   - succeeds with the single match.
//
// On success, emits `containerfile=<path>` via OutputSink.
//
// ValidateContainerfile checks the configured Containerfile exists.
func ValidateContainerfile(ctx context.Context, sink ci.OutputSink, out io.Writer, in ValidateContainerfileInput) error {
	if in.Path == "" {
		return fmt.Errorf("containerfile path is required: pass --path <file> or set $CONTAINERFILE: %w", errs.ErrUsage)
	}

	for _, dir := range in.SourceDirs {
		if !pathsafe.Relative(dir) {
			return fmt.Errorf("source directory %q must be a relative path inside the source checkout: %w", dir, errs.ErrValidation)
		}
	}

	emit := func(path string) error {
		if in.Context != nil {
			source, err := os.Getwd()
			if err != nil {
				return err
			}

			if err = ValidateSourceBuildPaths(source, *in.Context, path); err != nil {
				return err
			}
		}

		_, _ = fmt.Fprintf(out, "Using containerfile: %s\n", path)

		return sink.Set(ctx, "containerfile", path)
	}

	if info, err := os.Stat(in.Path); err == nil && !info.IsDir() {
		return emit(in.Path)
	}

	dir := filepath.Dir(in.Path)

	matches := findContainerfileCandidates(dir)
	switch len(matches) {
	case 0:
		return fmt.Errorf("containerfile %q not found and no Dockerfile*/Containerfile* match found in %q: %w", in.Path, dir, errs.ErrMissingInput)
	case 1:
		return emit(matches[0])
	default:
		return fmt.Errorf("multiple containerfiles found in %q, please specify an exact path:\n  %s: %w",
			dir, strings.Join(matches, "\n  "), errs.ErrValidation)
	}
}

// ValidateSourceBuildPaths keeps a build context and Containerfile inside the
// source checkout, separate from the delivery layer's control files.
func ValidateSourceBuildPaths(source, contextPath, file string) error {
	if !pathsafe.Relative(contextPath) || (file != "" && !pathsafe.Relative(file)) {
		return fmt.Errorf("build context and Containerfile must be source-relative paths: %w", errs.ErrValidation)
	}

	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}

	defer func() { _ = root.Close() }()

	if info, statErr := root.Stat(contextPath); statErr != nil || !info.IsDir() {
		return fmt.Errorf("build context must be a directory inside the source checkout: %w", errs.ErrValidation)
	}

	if file != "" {
		if info, statErr := root.Stat(file); statErr != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("containerfile must be a regular file inside the source checkout: %w", errs.ErrValidation)
		}
	}

	return nil
}

// findContainerfileCandidates scans dir (non-recursive) and returns
// matching files sorted for deterministic test output.
func findContainerfileCandidates(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var out []string

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		name := e.Name()
		if strings.HasPrefix(name, "Dockerfile") || strings.HasPrefix(name, "Containerfile") {
			out = append(out, filepath.Join(dir, name))
		}
	}

	slices.Sort(out)

	return out
}
