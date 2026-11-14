// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"io"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	appsecurity "github.com/diggsweden/forge-ci/internal/app/security"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
)

// BuildAndScanInput drives BuildAndScan: the build-image parameters plus the
// threshold of the vulnerability scan every exported layout gets.
type BuildAndScanInput struct {
	Build BuildImageInput

	// ScanSeverity is the scan's blocking threshold; see
	// appsecurity.ScanContainerInput.Severity.
	ScanSeverity string
	// TrivyVersion is embedded in the GitLab container-scanning report.
	TrivyVersion string
	// DeferScan exports an oci-mode layout unscanned: a later `security scan
	// container --oci-layout` job, in the full runtime image that carries
	// Trivy, scans it and gates publication. Refused in push-by-digest mode,
	// where nothing stands between the scan and the push.
	DeferScan bool
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
	if _, err := appenforcement.Resolve(ctx); err != nil {
		return "", err
	}

	if in.Build.Mode != domaincontainer.BuildModePushByDigest && in.Build.Mode != domaincontainer.BuildModeOCI {
		return BuildImage(ctx, builder, pusher, sink, out, in.Build)
	}

	if in.DeferScan {
		if in.Build.Mode != domaincontainer.BuildModeOCI {
			return "", fmt.Errorf("--defer-scan leaves the exported layout to a later scan job and is valid in oci mode only: %w", errs.ErrUsage)
		}

		_, _ = fmt.Fprintln(out, "Scan deferred: the layout scan job scans this exported layout before publication.")

		return buildImage(ctx, builder, pusher, sink, out, in.Build, nil)
	}

	if trivy == nil {
		return "", fmt.Errorf("admin policy requires a vulnerability scanner: %w", errs.ErrPrerequisite)
	}

	// --cache-to writes during the build, before a scan could refuse it.
	if in.Build.CachePush {
		in.Build.CachePush = false
		_, _ = fmt.Fprintln(out, "Registry cache export disabled for scan-before-publication; cache reads remain enabled.")
	}

	check := func(scanContext context.Context, directory string) error {
		return appsecurity.ScanContainer(scanContext, trivy, out, stderr, annot, appsecurity.ScanContainerInput{
			ImageRef: in.Build.ImageRef, OCILayout: directory, Platform: in.Build.Platform, Severity: in.ScanSeverity, TrivyVersion: in.TrivyVersion,
		})
	}

	return buildImage(ctx, builder, pusher, sink, out, in.Build, check)
}
