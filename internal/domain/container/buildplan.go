// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// BuildOutputMode selects what `container build` does with the built image.
// The modes are mutually exclusive by construction (a single field),
// mirroring the docker/build-push-action output shapes forge-ci uses.
type BuildOutputMode string

// Supported build output modes.
const (
	// BuildModePushByDigest pushes the per-arch image and returns its digest,
	// with no human tag of its own — the multi-arch index and the real tags are
	// applied later by ggcr `container manifest merge`. Mirrors build-push's
	// outputs=type=image,push-by-digest=true.
	BuildModePushByDigest BuildOutputMode = "push-by-digest"
	// BuildModeLoad builds into local container storage under a tag, for
	// smoke-testing (the self-runtime-container flow's load:true). No push.
	BuildModeLoad BuildOutputMode = "load"
	// BuildModeLocal exports a build stage's filesystem to a directory, for
	// binary extraction (build-push's outputs=type=local). No image, no push.
	BuildModeLocal BuildOutputMode = "local"
	// BuildModeOCI retains an OCI layout for a dependent publication job.
	// Registry cache export is disabled, including when no scan was requested.
	BuildModeOCI BuildOutputMode = "oci"
)

// BuildRequest is the tool-neutral description of one image build. The buildah
// adapter translates it to argv. The fields are the subset of
// docker/build-push-action forge-ci actually uses: a SINGLE native platform
// (the multi-arch index is assembled later by ggcr, so there is no QEMU
// cross-build here), secrets pre-materialized to 0600 tmpfiles, and a
// forge-neutral registry layer cache instead of the GitHub-specific type=gha.
type BuildRequest struct {
	AuthFile      string // explicit registry credentials for base/cache reads
	Context       string // build context directory (required)
	Containerfile string // path to the Containerfile/Dockerfile; empty → buildah's default lookup
	Target        string // multi-stage target stage; empty → final stage
	Platform      string // single platform, e.g. linux/arm64; empty → host

	BuildArgs []string // "KEY=VALUE" pairs
	Secrets   []string // "id=NAME,src=PATH" — the buildah/build-push format, emitted verbatim by `container materialize-build-secrets`, passed straight through
	Labels    []string // "key=value" OCI labels (from `container metadata`)

	SourceDateEpoch string // unix seconds; empty disables the reproducibility timestamp clamp

	// Forge-neutral registry layer cache (replacing the GitHub-specific type=gha).
	// CacheRepo is the dedicated cache repository, named without a tag: Buildah
	// keys cached layers by content and refuses a tagged cache reference, so
	// images and architectures share one repository without colliding. Import
	// is best-effort (a miss just builds from scratch); CachePush also EXPORTS
	// and is set only on a trusted push (never a fork PR). Empty CacheRepo
	// disables caching entirely.
	CacheRepo string
	CachePush bool

	Mode      BuildOutputMode
	ImageRef  string // push target (push-by-digest) or local tag (load)
	OutputDir string // destination directory (local or fresh oci layout)
}

// Validate checks mode-specific required fields and secret shape. The single
// Mode field already makes the output modes mutually exclusive.
func (r BuildRequest) Validate() error {
	if r.Context == "" {
		return fmt.Errorf("build context is required: %w", errs.ErrUsage)
	}

	switch r.Mode {
	case BuildModePushByDigest, BuildModeLoad:
		if r.ImageRef == "" {
			return fmt.Errorf("image reference is required for %s mode: %w", r.Mode, errs.ErrUsage)
		}
	case BuildModeLocal, BuildModeOCI:
		if r.OutputDir == "" {
			return fmt.Errorf("output directory is required for %s mode: %w", r.Mode, errs.ErrUsage)
		}
	default:
		return fmt.Errorf("unknown build output mode %q (want push-by-digest|load|local|oci): %w", r.Mode, errs.ErrUsage)
	}

	for _, s := range r.Secrets {
		if err := validateBuildSecret(s); err != nil {
			return err
		}
	}

	return nil
}

func validateBuildSecret(value string) error {
	fields := map[string]string{}

	for part := range strings.SplitSeq(value, ",") {
		key, val, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(val) == "" || fields[key] != "" || strings.ContainsAny(val, "\r\n\x00") {
			return fmt.Errorf("build secret must contain one nonempty id and src field: %w", errs.ErrUsage)
		}

		switch key {
		case "id", "src":
			fields[key] = val
		default:
			return fmt.Errorf("build secret contains an unsupported field: %w", errs.ErrUsage)
		}
	}

	if len(fields) != 2 {
		return fmt.Errorf("build secret requires id and src: %w", errs.ErrUsage)
	}

	if strings.ContainsAny(fields["id"], " /\\\t") {
		return fmt.Errorf("build secret id must be a single name: %w", errs.ErrUsage)
	}

	return nil
}
