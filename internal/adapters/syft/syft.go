// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package syft shells out to the system `syft` binary. The runtime
// image bakes syft in; local runs need it on PATH.
package syft

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// Adapter wraps the syft binary. Bin is overridable for tests.
type Adapter struct {
	Bin string // empty → "syft"
}

// New returns an Adapter using the system syft.
func New() *Adapter { return &Adapter{} }

// fileSchemes are the syft source schemes whose rest is a filesystem path.
var fileSchemes = []string{"dir:", "file:", "oci-dir:", "oci-archive:", "docker-archive:", "singularity:"}

// Generate scans target once and writes one SBOM file per (format,
// outputFile) entry in outputs (e.g. "spdx-json" → "/tmp/x.spdx.json").
// syft's native `-o format=file` flag supports multiple outputs in a
// single invocation, so the scan happens once regardless of how many
// formats are requested. Stderr is forwarded to errOut.
//
// Mirrors invocations, batched:
//
//	syft "$scan_target" -o spdx-json="$spdx_file" -o cyclonedx-json="$cdx_file"
//
// Map iteration order is non-deterministic; argv is built by walking
// sorted keys so test assertions and logs stay stable.
//
// syft runs away from the caller's directory, so relative output files and a
// relative path target, bare or behind a file scheme such as oci-dir:, are
// made absolute against it first.
func (a *Adapter) Generate(ctx context.Context, target string, outputs map[string]string, errOut io.Writer) error {
	if len(outputs) == 0 {
		return fmt.Errorf("syft: no outputs requested: %w", errs.ErrUsage)
	}

	formats := make([]string, 0, len(outputs))
	for f := range outputs {
		formats = append(formats, f)
	}

	slices.Sort(formats)

	source, err := absoluteSource(target)
	if err != nil {
		return err
	}

	args := []string{source}

	for _, f := range formats {
		file, err := filepath.Abs(outputs[f])
		if err != nil {
			return fmt.Errorf("syft output %s: %w", outputs[f], err)
		}

		args = append(args, "-o", f+"="+file)
	}

	return a.run(ctx, io.Discard, errOut, args)
}

// RunInherit invokes `syft` with args, streaming stdout/stderr to the
// provided writers. Used by callers that need the raw stdout (e.g.
// `syft --version`). Path arguments must be absolute.
func (a *Adapter) RunInherit(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	return a.run(ctx, stdout, stderr, args)
}

// run starts syft isolated the way the trivy adapter is: SYFT_* variables
// and the .syft.yaml of the caller's directory (the checkout) could exclude
// paths or catalogers from an SBOM the engine signs, so syft gets only
// safeexec.GateToolEnv, an explicit empty configuration file instead of the
// discovered ones, and a private empty working directory.
func (a *Adapter) run(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	dir, err := os.MkdirTemp("", "forge-ci-syft-*")
	if err != nil {
		return fmt.Errorf("create syft working directory: %w", err)
	}

	defer func() { _ = os.RemoveAll(dir) }()

	config := filepath.Join(dir, "syft.yaml")
	if err := os.WriteFile(config, []byte("{}\n"), 0o600); err != nil {
		return fmt.Errorf("write syft configuration: %w", err)
	}

	cmd := safeexec.Command(ctx, a.bin(), slices.Concat(args, []string{"--config", config})...)
	cmd.Env = safeexec.GateToolEnv(os.Environ())
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		// A non-exit failure (most commonly syft missing from PATH) is a
		// missing prerequisite, not an internal bug: EX_CONFIG (78), not the
		// unclassified EX_SOFTWARE (70).
		return safeexec.WrapError(err, a.bin(), safeexec.FirstArg(args))
	}

	return nil
}

// absoluteSource makes a path source absolute: the rest of a file scheme, or a
// bare target that exists on disk, which syft would read as a path. Anything
// else is an image reference and is returned as given.
func absoluteSource(target string) (string, error) {
	for _, scheme := range fileSchemes {
		if rest, ok := strings.CutPrefix(target, scheme); ok {
			path, err := filepath.Abs(rest)
			if err != nil {
				return "", fmt.Errorf("syft source %s: %w", target, err)
			}

			return scheme + path, nil
		}
	}

	if _, err := os.Lstat(target); err != nil {
		return target, nil //nolint:nilerr // not on disk: an image reference.
	}

	path, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("syft source %s: %w", target, err)
	}

	return path, nil
}

func (a *Adapter) bin() string {
	if a.Bin != "" {
		return a.Bin
	}

	return "syft"
}
