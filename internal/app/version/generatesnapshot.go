// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// GenerateSnapshotOps is the slice of git-adapter methods GenerateSnapshotVersion
// needs. Tests inject a fake; production passes adapter/git.New().
type GenerateSnapshotOps interface {
	Run(ctx context.Context, args ...string) (string, error)
	ListTags(ctx context.Context, pattern string) ([]string, error)
	ShortSHA(ctx context.Context, ref string, n int) (string, error)
}

// GenerateSnapshotVersionInput drives GenerateSnapshotVersion.
type GenerateSnapshotVersionInput struct {
	// ContainerTag selects the OCI-safe postfix form; package versions retain their historical form.
	ContainerTag bool
	// RefName is the source branch / ref to sanitise into the snapshot-version
	// suffix.
	RefName string

	// Format is the output encoding. Zero-value (FormatText / empty)
	// preserves the historical "one bare version line on w" shape;
	// FormatJSON emits {"version":"<dev>"}; CI-platform formats reuse
	// the bare w shape and additionally emit a named CI output when a
	// sink is supplied.
	Format output.Format
	Sink   ci.OutputSink
}

// GenerateSnapshotVersion prints `<base>-snapshot-<sanitised-branch>-<short-sha>`
// to w. Best-effort `git fetch --tags` is attempted; failures are
// ignored so the function still works on a shallow checkout that
// already has the tags it needs.
func GenerateSnapshotVersion(ctx context.Context, ops GenerateSnapshotOps, out io.Writer, in GenerateSnapshotVersionInput) error {
	if in.RefName == "" {
		return fmt.Errorf("ref-name is required: %w", errs.ErrUsage)
	}

	// A name made only of characters the sanitizer drops would leave an empty
	// segment: 0.0.0-snapshot--abc1234 identifies no branch.
	if version.SanitizePathToken(in.RefName) == "" {
		return fmt.Errorf("ref-name %q has no characters usable in a snapshot version: %w", in.RefName, errs.ErrUsage)
	}

	// Best-effort tag fetch — ignored on failure (shallow checkout).
	_, _ = ops.Run(ctx, "fetch", "--tags")

	tags, err := ops.ListTags(ctx, "v[0-9]*.[0-9]*.[0-9]*")
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}

	latest := version.LatestSemverTag(tags)

	base := version.SnapshotVersionDefaultBase
	if latest != "" {
		base = version.StripVPrefix(latest)
	}

	shortSHA, err := abbreviatedHead(ctx, ops)
	if err != nil {
		return err
	}

	dev := version.ComposeSnapshotVersion(base, in.RefName, shortSHA)
	if in.ContainerTag {
		dev, err = version.ComposeSnapshotContainerTag(base, in.RefName, shortSHA)
		if err != nil {
			return err
		}
	}
	if in.Sink != nil && (in.Format == output.FormatGitHub || in.Format == output.FormatGitLab) {
		if err := in.Sink.Set(ctx, "snapshot-version", dev); err != nil {
			return err
		}
	}

	return writeSnapshotVersion(out, dev, in.Format)
}

// abbreviatedHead returns HEAD's abbreviated commit. git lengthens an
// abbreviation to keep it unique, never shortens it, and prints lowercase hex;
// anything else is not a commit it resolved.
func abbreviatedHead(ctx context.Context, ops GenerateSnapshotOps) (string, error) {
	shortSHA, err := ops.ShortSHA(ctx, "HEAD", version.SnapshotShortSHALen)
	if err != nil {
		return "", fmt.Errorf("rev-parse --short HEAD: %w", err)
	}

	if len(shortSHA) < version.SnapshotShortSHALen || strings.Trim(shortSHA, "0123456789abcdef") != "" {
		return "", fmt.Errorf("rev-parse --short HEAD returned %q, not an abbreviated commit: %w", shortSHA, errs.ErrMalformedInput)
	}

	return shortSHA, nil
}

// writeSnapshotVersion renders dev according to format. The historical
// shape is a bare line on w; FormatJSON wraps in a one-key
// object. Unknown formats fall back to the bare line.
func writeSnapshotVersion(out io.Writer, dev string, format output.Format) error {
	if format == output.FormatJSON {
		body, err := json.Marshal(struct {
			Version string `json:"version"`
		}{Version: dev})
		if err != nil {
			return fmt.Errorf("encode dev version json: %w", err)
		}

		_, err = fmt.Fprintln(out, string(body))

		return err
	}

	_, err := fmt.Fprintln(out, dev)

	return err
}
