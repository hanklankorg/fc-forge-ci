// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"io"

	appsecurity "github.com/diggsweden/forge-ci/internal/app/security"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
)

// BuildAndScanInput drives BuildAndScan: the build-image parameters plus the
// in-build vulnerability scan toggle/threshold.
type BuildAndScanInput struct {
	Build BuildImageInput

	// EnableScan is retained for callers; false cannot disable admin-required
	// scan execution for exported/published layouts.
	EnableScan bool
	// ScanSeverity is the trivy --severity filter AND fail-on threshold.
	ScanSeverity string
	// TrivyVersion is embedded in the GitLab container-scanning report.
	TrivyVersion string
}

// BuildAndScan builds one platform, scans its exported layout, then publishes
// those same bytes. Failed scans emit no digest and never reach the pusher.
// The successful digest is emitted for the downstream cross-platform
// `container manifest merge`, and the scan writes its SARIF + GitLab
// container-scanning report at the default paths for the forge job to upload.
//
// This is the container sibling of the language `build <eco> run` consolidation:
// the build-logic sequence is owned in Go; the forge job keeps only the
// platform-transport steps (login, artifact download/upload) around it.
func BuildAndScan(
	ctx context.Context,
	builder imageBuilder,
	pusher layoutPusher,
	trivy appsecurity.TrivyOps,
	sink ci.OutputSink,
	out, stderr io.Writer,
	annot output.Annotator,
	in BuildAndScanInput,
) (string, error) {
	if _, err := enforcement.Current(); err != nil {
		return "", err
	}
	if in.Build.Mode != domaincontainer.BuildModePushByDigest && in.Build.Mode != domaincontainer.BuildModeOCI {
		return BuildImage(ctx, builder, pusher, sink, out, in.Build)
	}
	if trivy == nil {
		return "", fmt.Errorf("admin policy requires a vulnerability scanner: %w", errs.ErrDependencyUnavailable)
	}
	if !in.EnableScan {
		_, _ = fmt.Fprintln(out, "Consumer enable-scan=false ignored: admin policy requires scan execution.")
	}

	// --cache-to writes during the build, before a scan could refuse it.
	if in.Build.CachePush {
		in.Build.CachePush = false
		_, _ = fmt.Fprintln(out, "Registry cache export disabled for scan-before-publication; cache reads remain enabled.")
	}

	check := func(scanContext context.Context, directory string) error {
		return appsecurity.ScanContainer(scanContext, trivy, out, stderr, annot, appsecurity.ScanContainerInput{
			ImageRef: in.Build.ImageRef, OCILayout: directory, Severity: in.ScanSeverity, TrivyVersion: in.TrivyVersion,
		})
	}

	return buildImage(ctx, builder, pusher, sink, out, in.Build, check)
}
