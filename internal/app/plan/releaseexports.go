// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ReleaseExportsInput admits export jobs before release preparation mutates Git.
type ReleaseExportsInput struct {
	Enabled, JobImage, Platforms, ARM64Tags, Commands string
	ImagePlan                                         []byte
}

// ReleaseExportPlatforms shares the native platform subset validator. Empty
// export selection retains its existing amd64 default, unlike image defaults.
func ReleaseExportPlatforms(selection string) ([]string, error) {
	if selection == "" {
		selection = "linux/amd64"
	}

	return domaincontainer.SelectPlatforms([]string{"linux/amd64", "linux/arm64"}, selection)
}

// CheckReleaseExports validates only data; export commands never execute here.
func CheckReleaseExports(in ReleaseExportsInput) error {
	var commands []string
	if strings.TrimSpace(in.Commands) != "" {
		if err := json.Unmarshal([]byte(in.Commands), &commands); err != nil {
			return fmt.Errorf("export commands must be a JSON string array: %w", errs.ErrUsage)
		}

		if commands == nil {
			return fmt.Errorf("export commands must be an array, not null: %w", errs.ErrUsage)
		}
	}

	switch in.Enabled {
	case "false":
		if len(commands) != 0 {
			return fmt.Errorf("export-files is false but export commands are set: %w", errs.ErrUsage)
		}

		return nil
	case "true":
	default:
		return fmt.Errorf("export-files must be true or false: %w", errs.ErrUsage)
	}

	if !domaincontainer.ValidDigestReference(in.JobImage) {
		return fmt.Errorf("export-files requires builder-job-image pinned by digest: %w", errs.ErrUsage)
	}

	platforms, err := ReleaseExportPlatforms(in.Platforms)
	if err != nil {
		return err
	}

	var tags []string
	if err := json.Unmarshal([]byte(in.ARM64Tags), &tags); err != nil {
		return fmt.Errorf("build-runner-tags-arm64 must be a JSON string array: %w", errs.ErrUsage)
	}

	if tags == nil {
		return fmt.Errorf("build-runner-tags-arm64 must be an array, not null: %w", errs.ErrUsage)
	}

	if slices.Contains(platforms, "linux/arm64") && len(tags) == 0 {
		return fmt.Errorf("linux/arm64 requires build-runner-tags-arm64: %w", errs.ErrUsage)
	}

	if len(in.ImagePlan) != 0 {
		var plan struct {
			Builds []struct {
				Arch string `json:"arch"`
			} `json:"builds"`
		}
		if err := json.Unmarshal(in.ImagePlan, &plan); err != nil {
			return fmt.Errorf("release image plan: %w", err)
		}

		for _, build := range plan.Builds {
			if build.Arch != "amd64" && build.Arch != "arm64" {
				return fmt.Errorf("unknown image architecture %q: %w", build.Arch, errs.ErrValidation)
			}

			if !slices.Contains(platforms, "linux/"+build.Arch) {
				return fmt.Errorf("image and export architectures differ; set release-platforms: %w", errs.ErrValidation)
			}
		}
	}

	return nil
}
