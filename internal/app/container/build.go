// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/diggsweden/forge-ci/internal/clicolor"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// imageBuilder is the slice of adapters/buildah.Adapter that BuildImage drives.
// Build handles the load/local modes; BuildToLayout stages a push-by-digest
// build into an OCI layout for the pusher. Narrow port so app-layer tests fake
// the build without invoking buildah.
type imageBuilder interface {
	Build(ctx context.Context, req container.BuildRequest, out io.Writer) error
	BuildToLayout(ctx context.Context, req container.BuildRequest, layoutDir string, out io.Writer) error
}

// layoutPusher is the slice of adapters/ociregistry.Adapter that BuildImage
// uses to publish a staged build. It writes the image TAGLESS by digest (the
// daemonless push-by-digest), returning that digest.
type layoutPusher interface {
	PushLayoutByDigest(ctx context.Context, layoutDir, imageRef string) (string, error)
}

// BuildImageInput drives `forge-ci container build`. Mirrors the workflow
// env contract and maps 1:1 onto domain/container.BuildRequest.
type BuildImageInput struct {
	AuthFile      string
	Context       string
	Containerfile string
	Target        string
	Platform      string
	// HostArch is the GOARCH this engine binary was compiled for. The layer
	// runs only the binary of its host's architecture, so a non-empty
	// Platform must name it: that is what refuses a build whose steps would
	// run emulated.
	HostArch  string
	BuildArgs []string
	Secrets   []string
	Labels    []string

	SourceDateEpoch string
	CacheRepo       string
	CachePush       bool

	Mode      container.BuildOutputMode
	ImageRef  string
	OutputDir string

	// DigestKey is the OutputSink key for the pushed digest (push-by-digest
	// mode). Empty defaults to "digest".
	DigestKey string
}

// BuildImage builds one image. For push-by-digest it stages the build into an
// OCI layout with buildah and pushes it TAGLESS by digest with ggcr (no daemon,
// no GitHub action, no ephemeral tag to clean up), recording the digest on the
// OutputSink so the downstream `container manifest merge` can reference it.
// Load and local modes build in place and push nothing. OCI mode retains a fresh
// layout without registry writes; `container layout record` binds its identity
// for the dependent publication job after the configured scan succeeds.
func BuildImage(
	ctx context.Context,
	builder imageBuilder,
	pusher layoutPusher,
	sink ci.OutputSink,
	out io.Writer, //nolint:varnamelen // idiomatic short name (io conventions).
	in BuildImageInput,
) (string, error) {
	return buildImage(ctx, builder, pusher, sink, out, in, nil)
}

func buildImage(ctx context.Context, builder imageBuilder, pusher layoutPusher, sink ci.OutputSink, out io.Writer, in BuildImageInput, checkLayout func(context.Context, string) error) (string, error) {
	req := buildRequest(in)
	if err := validateBuild(req, in.HostArch); err != nil {
		return "", err
	}

	if req.Mode == container.BuildModeOCI {
		return "", stageBuild(ctx, builder, req, out, checkLayout)
	}

	if req.Mode != container.BuildModePushByDigest {
		if err := builder.Build(ctx, req, out); err != nil {
			return "", fmt.Errorf("build image: %w", err)
		}

		reportBuild(out, req, "")

		return "", nil
	}

	digest, err := pushByDigest(ctx, builder, pusher, req, out, checkLayout)
	if err != nil {
		return "", err
	}

	if !container.ValidDigest(digest) {
		return "", fmt.Errorf("image push returned a malformed digest: %w", errs.ErrInvalidConfig)
	}

	if sink != nil {
		key := in.DigestKey
		if key == "" {
			key = outputKeyDigest
		}

		if err := sink.Set(ctx, key, digest); err != nil {
			return "", fmt.Errorf("emit digest: %w", err)
		}
	}

	reportBuild(out, req, digest)

	return digest, nil
}

// validateBuild checks the request, then refuses a platform whose
// architecture is not the host's. An empty platform is Buildah's host
// default, a native build.
func validateBuild(req container.BuildRequest, hostArch string) error {
	if err := req.Validate(); err != nil {
		return err
	}

	if req.Platform == "" {
		return nil
	}

	parsed, err := container.ParsePlatform(req.Platform)
	if err != nil {
		return err
	}

	return container.RequireNativeArch(parsed.Arch, hostArch)
}

// stageBuild owns a fresh output directory. A failed build or scan cannot leave
// an artifact eligible for the later record/upload steps. Cache reads remain
// useful; cache writes would violate the cross-job publication barrier.
func stageBuild(ctx context.Context, builder imageBuilder, req container.BuildRequest, out io.Writer, checkLayout func(context.Context, string) error) error {
	parent, err := pathsafe.MkdirRoot(filepath.Dir(req.OutputDir), 0o755)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()

	base := filepath.Base(req.OutputDir)
	if err := parent.Mkdir(base, 0o700); err != nil {
		return fmt.Errorf("OCI output must be a fresh directory: %w: %w", err, errs.ErrUsage)
	}

	complete := false
	defer func() {
		if !complete {
			_ = parent.RemoveAll(base)
		}
	}()

	req.CachePush = false
	if err := builder.BuildToLayout(ctx, req, req.OutputDir, out); err != nil {
		return fmt.Errorf("stage image: %w", err)
	}

	if checkLayout != nil {
		if err := checkLayout(ctx, req.OutputDir); err != nil {
			return err
		}
	}

	complete = true

	reportBuild(out, req, "")

	return nil
}

// pushByDigest stages the build into a temp OCI layout and pushes it tagless.
func pushByDigest(
	ctx context.Context,
	builder imageBuilder,
	pusher layoutPusher,
	req container.BuildRequest,
	out io.Writer, //nolint:varnamelen // idiomatic short name (io conventions).
	checkLayout func(context.Context, string) error,
) (string, error) {
	layoutDir, err := os.MkdirTemp("", "forge-ci-oci-*")
	if err != nil {
		return "", fmt.Errorf("create oci layout dir: %w", err)
	}

	defer func() { _ = os.RemoveAll(layoutDir) }()

	if err = builder.BuildToLayout(ctx, req, layoutDir, out); err != nil {
		return "", fmt.Errorf("build image: %w", err)
	}

	if checkLayout != nil {
		if err = checkLayout(ctx, layoutDir); err != nil {
			return "", err
		}
	}

	digest, err := pusher.PushLayoutByDigest(ctx, layoutDir, req.ImageRef)
	if err != nil {
		return "", fmt.Errorf("push by digest: %w", err)
	}

	return digest, nil
}

func buildRequest(in BuildImageInput) container.BuildRequest {
	return container.BuildRequest{
		AuthFile:        in.AuthFile,
		Context:         in.Context,
		Containerfile:   in.Containerfile,
		Target:          in.Target,
		Platform:        in.Platform,
		BuildArgs:       in.BuildArgs,
		Secrets:         in.Secrets,
		Labels:          in.Labels,
		SourceDateEpoch: in.SourceDateEpoch,
		CacheRepo:       in.CacheRepo,
		CachePush:       in.CachePush,
		Mode:            in.Mode,
		ImageRef:        in.ImageRef,
		OutputDir:       in.OutputDir,
	}
}

func reportBuild(out io.Writer, req container.BuildRequest, digest string) {
	check := clicolor.Check(out)

	switch req.Mode {
	case container.BuildModePushByDigest:
		_, _ = fmt.Fprintf(out, "%s Built and pushed by digest %s → %s\n", check, req.ImageRef, digest)
	case container.BuildModeLoad:
		_, _ = fmt.Fprintf(out, "%s Built %s into local storage\n", check, req.ImageRef)
	case container.BuildModeLocal:
		_, _ = fmt.Fprintf(out, "%s Built and exported to %s\n", check, req.OutputDir)
	case container.BuildModeOCI:
		_, _ = fmt.Fprintf(out, "%s Built OCI layout at %s; publication deferred.\n", check, req.OutputDir)
	}
}
