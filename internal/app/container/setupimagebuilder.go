// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// SetupImageBuilderInput selects the existing Buildah setup or admission of an
// already-provisioned Kaniko job image. It never builds or publishes an image.
type SetupImageBuilderInput struct {
	Builder, JobImage, EngineRepository, Arch string
	RequireJobImage                           bool
	Buildah                                   SetupBuildahInput
}

// SetupImageBuilder owns backend selection and admission formerly in the
// Forgejo action. Storage setup and image inspection reuse their existing owners.
func SetupImageBuilder(ctx context.Context, tool BuildahSetupTool, installer PackageInstaller, registry UsableImageDigestRegistry, sink ci.OutputSink, summary ci.SummarySink, out io.Writer, in SetupImageBuilderInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// An empty CI_IMAGE_BUILDER reaches the flag as "", not as its default.
	builder := cmp.Or(in.Builder, "buildah")
	if builder != "buildah" && builder != "kaniko" {
		return fmt.Errorf("image-builder must be buildah or kaniko, got %q: %w", in.Builder, errs.ErrUsage)
	}

	if in.RequireJobImage && !domaincontainer.ValidDigestReference(in.JobImage) {
		return fmt.Errorf("%s needs builder-job-image pinned by digest, got %q: %w", builder, in.JobImage, errs.ErrUsage)
	}

	if builder == "kaniko" {
		return admitKanikoJobImage(ctx, registry, sink, in)
	}

	_, err := SetupBuildah(ctx, tool, installer, sink, summary, out, in.Buildah)

	return err
}

func admitKanikoJobImage(ctx context.Context, registry UsableImageDigestRegistry, sink ci.OutputSink, in SetupImageBuilderInput) error {
	if !domaincontainer.ValidDigestReference(in.JobImage) {
		return fmt.Errorf("kaniko needs CI_IMAGE_BUILDER_JOB_IMAGE pinned by digest: %w", errs.ErrUsage)
	}

	if in.Arch != domaincontainer.ArchAMD64 && in.Arch != domaincontainer.ArchARM64 {
		return fmt.Errorf("no kaniko job image for this architecture: %w", errs.ErrUsage)
	}

	source := strings.TrimSuffix(in.EngineRepository, ".git")

	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || strings.ContainsAny(source, " \t\r\n") {
		return fmt.Errorf("kaniko admission requires a plain HTTPS engine repository URL: %w", errs.ErrUsage)
	}

	_, err = UsableImageDigest(ctx, registry, sink, UsableImageDigestInput{
		Ref: in.JobImage, Arch: in.Arch,
		RequiredLabels: []string{"org.opencontainers.image.source=" + source, "org.opencontainers.image.title=forge-ci kaniko job image"},
	})

	return err
}
