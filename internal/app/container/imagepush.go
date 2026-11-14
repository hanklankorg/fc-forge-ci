// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// ImagePushTool is the Buildah surface needed to push one local image.
type ImagePushTool interface {
	ExportImageToLayout(ctx context.Context, authFile string, tlsVerify bool, image, layoutDir string, out io.Writer) error
}

// ImagePublishRegistry publishes the exported bytes and supplies their read-back.
type ImagePublishRegistry interface {
	RawManifestRegistry
	PushLayout(ctx context.Context, layoutDir, destination string) (string, error)
}

// PushImageInput drives `container image push`.
type PushImageInput struct {
	LocalImage    string
	Destination   string
	AuthFile      string
	TLSVerify     string
	RetryAttempts int
	RetryDelay    time.Duration
}

// PushImageOutput is the pushed image digest and digest-pinned ref.
type PushImageOutput struct {
	Digest string
	Ref    string
}

// PushImage pushes one local Buildah image to a registry ref, verifies the raw
// manifest digest now served by the registry, and emits digest/ref outputs.
func PushImage(ctx context.Context, tool ImagePushTool, registry ImagePublishRegistry, sink ci.OutputSink, out io.Writer, in PushImageInput) (*PushImageOutput, error) {
	if err := validatePushImageInput(tool, registry, in); err != nil {
		return nil, err
	}

	layoutDir, err := os.MkdirTemp("", "forge-ci-image-push-*")
	if err != nil {
		return nil, fmt.Errorf("create publication layout: %w", err)
	}
	defer func() { _ = os.RemoveAll(layoutDir) }()

	if exportErr := tool.ExportImageToLayout(ctx, in.AuthFile, in.TLSVerify == tlsVerifyTrue, in.LocalImage, layoutDir, out); exportErr != nil {
		return nil, exportErr
	}

	layoutDigest, err := retry.Do(ctx, out, retry.Attempts(in.RetryAttempts, defaultBuildPushRetryAttempts), retry.Delay(in.RetryDelay, defaultBuildPushRetryDelay), func() (string, error) {
		return registry.PushLayout(ctx, layoutDir, in.Destination)
	})
	if err != nil {
		return nil, err
	}

	registryDigest, err := verifyPublishedLayout(ctx, registry, out, in.Destination, layoutDigest, in.RetryAttempts, in.RetryDelay)
	if err != nil {
		return nil, err
	}

	result := &PushImageOutput{Digest: registryDigest, Ref: domaincontainer.StripDigest(in.Destination) + "@" + registryDigest}
	if outputErr := emitPublishedReference(ctx, sink, result.Digest, result.Ref); outputErr != nil {
		return nil, outputErr
	}

	return result, nil
}

func validatePushImageInput(tool ImagePushTool, registry RawManifestRegistry, in PushImageInput) error {
	if tool == nil {
		return fmt.Errorf("container image push: buildah adapter is required: %w", errs.ErrUsage)
	}

	if registry == nil {
		return fmt.Errorf("container image push: registry adapter is required: %w", errs.ErrUsage)
	}

	if strings.TrimSpace(in.LocalImage) == "" {
		return fmt.Errorf("container image push: local image is required: %w", errs.ErrUsage)
	}

	if canonical, err := domaincontainer.CanonicalImageRef(in.LocalImage); err != nil || canonical != in.LocalImage {
		return fmt.Errorf("container image push: local image must be a safe OCI reference: %w", errs.ErrUsage)
	}

	if _, err := domaincontainer.CanonicalImageRef(in.Destination); err != nil || strings.TrimSpace(in.Destination) != in.Destination {
		return fmt.Errorf("container image push: destination ref is empty or unsafe: %w", errs.ErrUsage)
	}

	switch in.TLSVerify {
	case tlsVerifyTrue, tlsVerifyFalse:
		return nil
	default:
		return fmt.Errorf("container image push: tls-verify must be 'true' or 'false', got %q: %w", in.TLSVerify, errs.ErrUsage)
	}
}
