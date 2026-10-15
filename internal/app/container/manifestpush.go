// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// ManifestPushTool is the Buildah surface needed to push a local manifest list.
type ManifestPushTool interface {
	ExportManifestToLayout(ctx context.Context, authFile string, tlsVerify bool, manifest, layoutDir string, out io.Writer) error
	RemoveLocalManifest(ctx context.Context, manifest string, out io.Writer) error
}

// ManifestPublishRegistry publishes a complete exported index and reads it back.
type ManifestPublishRegistry interface {
	RawManifestRegistry
	PushIndexLayout(ctx context.Context, layoutDir, destination string) (string, error)
}

// PushManifestInput drives `container manifest push`.
type PushManifestInput struct {
	LocalManifest string
	Destination   string
	AuthFile      string
	TLSVerify     string
	RemoveLocal   bool
	RetryAttempts int
	RetryDelay    time.Duration
}

// PushManifestOutput is the pushed manifest digest and digest-pinned ref.
type PushManifestOutput struct {
	Digest string
	Ref    string
}

// PushedManifestDigestInput drives `container manifest digest`.
type PushedManifestDigestInput struct {
	Ref           string
	DigestFile    string
	RetryAttempts int
	RetryDelay    time.Duration
}

// PushedManifestDigestOutput is the verified registry digest and digest-pinned ref.
type PushedManifestDigestOutput struct {
	Digest string
	Ref    string
}

// PushManifest pushes a local Buildah manifest list to a registry ref, verifies
// the digest the registry serves, and emits digest/ref outputs.
func PushManifest(ctx context.Context, tool ManifestPushTool, registry ManifestPublishRegistry, sink ci.OutputSink, out io.Writer, in PushManifestInput) (*PushManifestOutput, error) {
	if err := validatePushManifestInput(tool, registry, in); err != nil {
		return nil, err
	}

	layoutDir, err := os.MkdirTemp("", "forge-ci-manifest-push-*")
	if err != nil {
		return nil, fmt.Errorf("create manifest publication layout: %w", err)
	}
	defer func() { _ = os.RemoveAll(layoutDir) }()

	if exportErr := tool.ExportManifestToLayout(ctx, in.AuthFile, in.TLSVerify == tlsVerifyTrue, in.LocalManifest, layoutDir, out); exportErr != nil {
		return nil, exportErr
	}

	layoutDigest, err := retry.Do(ctx, out, retry.Attempts(in.RetryAttempts, defaultBuildPushRetryAttempts), retry.Delay(in.RetryDelay, defaultBuildPushRetryDelay), func() (string, error) {
		return registry.PushIndexLayout(ctx, layoutDir, in.Destination)
	})
	if err != nil {
		return nil, err
	}

	registryDigest, err := verifyPublishedLayout(ctx, registry, out, in.Destination, layoutDigest, in.RetryAttempts, in.RetryDelay)
	if err != nil {
		return nil, err
	}

	if in.RemoveLocal {
		if removeErr := tool.RemoveLocalManifest(ctx, in.LocalManifest, out); removeErr != nil {
			return nil, fmt.Errorf("remove published local manifest: %w", removeErr)
		}
	}

	result := &PushManifestOutput{Digest: registryDigest, Ref: domaincontainer.StripDigest(in.Destination) + "@" + registryDigest}
	if outputErr := emitPublishedReference(ctx, sink, result.Digest, result.Ref); outputErr != nil {
		return nil, outputErr
	}

	return result, nil
}

// ResolvePushedManifestDigest resolves the registry-served raw manifest digest
// for a ref and, when a digest file is provided, verifies it matches the
// registry. This is the generic digestfile-vs-registry check used after Buildah
// image or manifest pushes.
func ResolvePushedManifestDigest(ctx context.Context, registry RawManifestRegistry, sink ci.OutputSink, out io.Writer, in PushedManifestDigestInput) (*PushedManifestDigestOutput, error) {
	if err := validatePushedManifestDigestInput(registry, in); err != nil {
		return nil, err
	}

	expectedDigest := ""

	warnOnInvalidExpected := false
	if in.DigestFile != "" {
		warnOnInvalidExpected = true

		body, err := os.ReadFile(in.DigestFile) //nolint:gosec // operator-selected Buildah digestfile path.
		if err == nil {
			expectedDigest = string(body)
		}
	}

	digest, err := resolveVerifiedManifestDigest(ctx, registry, out, in.Ref, expectedDigest, warnOnInvalidExpected, in.RetryAttempts, in.RetryDelay)
	if err != nil {
		return nil, err
	}

	result := &PushedManifestDigestOutput{Digest: digest, Ref: domaincontainer.StripDigest(in.Ref) + "@" + digest}
	if sink != nil {
		if err := sink.Set(ctx, outputKeyDigest, result.Digest); err != nil {
			return nil, err
		}

		if err := sink.Set(ctx, "ref", result.Ref); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func validatePushManifestInput(tool ManifestPushTool, registry RawManifestRegistry, in PushManifestInput) error {
	if tool == nil {
		return fmt.Errorf("container manifest push: buildah adapter is required: %w", errs.ErrUsage)
	}

	if registry == nil {
		return fmt.Errorf("container manifest push: registry adapter is required: %w", errs.ErrUsage)
	}

	if canonical, err := domaincontainer.CanonicalImageRef(in.LocalManifest); err != nil || canonical != in.LocalManifest {
		return fmt.Errorf("container manifest push: local manifest must be a safe OCI reference: %w", errs.ErrUsage)
	}

	if strings.TrimSpace(in.Destination) == "" || strings.ContainsAny(in.Destination, " \t\n\r") {
		return fmt.Errorf("container manifest push: destination ref is empty or unsafe: %w", errs.ErrUsage)
	}

	if _, err := domaincontainer.CanonicalImageRef(in.Destination); err != nil {
		return err
	}

	switch in.TLSVerify {
	case tlsVerifyTrue, tlsVerifyFalse:
		return nil
	default:
		return fmt.Errorf("container manifest push: tls-verify must be 'true' or 'false', got %q: %w", in.TLSVerify, errs.ErrUsage)
	}
}

func validatePushedManifestDigestInput(registry RawManifestRegistry, in PushedManifestDigestInput) error {
	if registry == nil {
		return fmt.Errorf("container manifest digest: registry adapter is required: %w", errs.ErrUsage)
	}

	if strings.TrimSpace(in.Ref) == "" || strings.ContainsAny(in.Ref, " \t\n\r") {
		return fmt.Errorf("container manifest digest: ref is empty or unsafe: %w", errs.ErrUsage)
	}

	return nil
}

func emitPublishedReference(ctx context.Context, sink ci.OutputSink, digest, ref string) error {
	if sink == nil {
		return nil
	}

	if err := sink.Set(ctx, outputKeyDigest, digest); err != nil {
		return err
	}

	return sink.Set(ctx, "ref", ref)
}

// A publisher's content digest is mandatory. The optional legacy digest-file
// fallback belongs only to the standalone manifest-digest command.
func verifyPublishedLayout(ctx context.Context, registry RawManifestRegistry, out io.Writer, ref, expected string, attempts int, delay time.Duration) (string, error) {
	if !domaincontainer.ValidDigest(expected) {
		return "", fmt.Errorf("layout publisher returned an invalid digest: %w", errs.ErrInvalidConfig)
	}

	return resolveVerifiedManifestDigest(ctx, registry, out, ref, expected, false, attempts, delay)
}

func resolveVerifiedManifestDigest(ctx context.Context, registry RawManifestRegistry, out io.Writer, ref, expectedDigest string, warnOnInvalidExpected bool, retryAttempts int, retryDelay time.Duration) (string, error) {
	expectedDigest = strings.TrimSpace(expectedDigest)
	if !domaincontainer.ValidDigest(expectedDigest) {
		if warnOnInvalidExpected && out != nil {
			_, _ = fmt.Fprintf(out, "Pushed manifest digest file was empty or invalid for %s; reading digest from registry.\n", ref)
		}

		expectedDigest = ""
	}

	registryDigest, err := retry.Do(ctx, out, retry.Attempts(retryAttempts, defaultBuildPushRetryAttempts), retry.Delay(retryDelay, defaultBuildPushRetryDelay), func() (string, error) {
		return registryManifestDigest(ctx, registry, ref)
	})
	if err != nil {
		return "", err
	}

	registryDigest = strings.TrimSpace(registryDigest)
	if !domaincontainer.ValidDigest(registryDigest) {
		return "", fmt.Errorf("registry returned an invalid manifest digest for %s: %q: %w", ref, registryDigest, errs.ErrInvalidConfig)
	}

	if expectedDigest != "" && expectedDigest != registryDigest {
		return "", fmt.Errorf("pushed manifest digest file does not match registry digest for %s\n  digest file: %s\n  registry:    %s: %w", ref, expectedDigest, registryDigest, errs.ErrValidation)
	}

	return registryDigest, nil
}

func registryManifestDigest(ctx context.Context, registry RawManifestRegistry, ref string) (string, error) {
	raw, err := registry.Manifest(ctx, ref)
	if err != nil {
		return "", err
	}

	if len(raw) == 0 {
		return "", fmt.Errorf("registry returned an empty manifest for %s: %w", ref, errs.ErrDependencyUnavailable)
	}

	sum := sha256.Sum256(raw)

	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
