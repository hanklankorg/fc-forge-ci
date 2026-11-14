// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
	"github.com/diggsweden/forge-ci/internal/listval"
)

// PlatformPlanInput drives `container platform-plan`.
type PlatformPlanInput struct {
	Platforms string
	Platform  string
	// ReleasePlatforms is RELEASE_PLATFORMS: the subset of Platforms this run builds.
	// Empty builds every one of them.
	ReleasePlatforms string
	// Runners is the calling layer's runner inputs. Each planned platform is
	// built natively on its own image build runner; nil checks nothing.
	Runners *runners.Inputs
}

// PlatformLeg is one entry of the matrix: the platform and the suffix its
// artifacts, caches and binaries are named by.
type PlatformLeg struct {
	Platform string `json:"platform"`
	Suffix   string `json:"suffix"`
}

// PlatformPlanOutput is the normalized platform matrix and optional arch suffix.
type PlatformPlanOutput struct {
	Platforms []string `json:"platforms"`
	Suffix    string   `json:"suffix,omitempty"`
}

// PlatformPlan writes platform matrix/suffix values to the output sink.
//
//nolint:cyclop // selects platforms per (CI provider, refType, override) combination.
func PlatformPlan(ctx context.Context, sink ci.OutputSink, writer io.Writer, in PlatformPlanInput) (*PlatformPlanOutput, error) {
	platforms, err := splitPlatforms(in.Platforms)
	if err != nil {
		return nil, err
	}

	platforms, err = domaincontainer.SelectPlatforms(platforms, in.ReleasePlatforms)
	if err != nil {
		return nil, err
	}

	// Before any output: a leg never runs on another platform's runner.
	needs := make([]runners.Need, 0, len(platforms))
	for _, planned := range platforms {
		needs = append(needs, runners.Need{Role: runners.Build, Platform: planned, Jobs: "image builds"})
	}

	if err := in.Runners.Admit(needs); err != nil {
		return nil, err
	}

	platform := strings.TrimSpace(in.Platform)
	if platform != "" {
		if err := validateNativePlatform(platform); err != nil {
			return nil, err
		}
	}

	out := &PlatformPlanOutput{
		Platforms: platforms,
		Suffix:    PlatformSuffix(platform),
	}
	if len(out.Platforms) > 0 {
		body, err := json.Marshal(out.Platforms)
		if err != nil {
			return nil, err
		}

		if err = sink.Set(ctx, "platforms-json", string(body)); err != nil {
			return nil, err
		}

		legs := make([]PlatformLeg, 0, len(out.Platforms))
		for _, leg := range out.Platforms {
			legs = append(legs, PlatformLeg{Platform: leg, Suffix: PlatformSuffix(leg)})
		}

		body, err = json.Marshal(legs)
		if err != nil {
			return nil, err
		}

		if err = sink.Set(ctx, "matrix-json", string(body)); err != nil {
			return nil, err
		}
	}

	if out.Suffix != "" {
		if err := sink.Set(ctx, "suffix", out.Suffix); err != nil {
			return nil, err
		}
	}

	if writer != nil {
		if len(out.Platforms) > 0 {
			_, _ = fmt.Fprintf(writer, "platforms: %s\n", strings.Join(out.Platforms, ","))
		}

		if out.Suffix != "" {
			_, _ = fmt.Fprintf(writer, "suffix: %s\n", out.Suffix)
		}
	}

	return out, nil
}

func splitPlatforms(value string) ([]string, error) {
	var out []string

	for _, raw := range listval.Tokens(value) {
		platform := strings.TrimSpace(raw)
		if platform == "" {
			continue
		}

		if err := validateNativePlatform(platform); err != nil {
			return nil, err
		}

		out = append(out, platform)
	}

	if len(out) == 0 && strings.TrimSpace(value) == "" {
		out = append(out, build.DefaultPlatform)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("platforms is empty: %w", errs.ErrUsage)
	}

	return out, nil
}

func validateNativePlatform(platform string) error {
	switch platform {
	case "linux/amd64", "linux/arm64":
		return nil
	default:
		return fmt.Errorf("unsupported container platform %q: native builds currently support linux/amd64 and linux/arm64: %w", platform, errs.ErrValidation)
	}
}

// PlatformSuffix returns the per-platform artifact/cache suffix.
func PlatformSuffix(platform string) string {
	platform = strings.TrimSpace(platform)
	platform = strings.TrimPrefix(platform, "linux/")
	platform = strings.ReplaceAll(platform, "/", "-")

	return platform
}
