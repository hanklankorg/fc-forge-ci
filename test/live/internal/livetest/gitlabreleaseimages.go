// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// DefaultArtifactsConfig is the declaration a forge's release and snapshot
// read when their artifacts-config input is empty: GitLab's release
// component, as templates/release.yml documents, GitHub's release
// orchestrator, and the snapshot-images workflow and component.
const DefaultArtifactsConfig = ".forge-ci/artifacts.yml"

// GitLabDedicatedReleaseImagesConfig is a consumer's separate release-image
// declaration. It is for a consumer whose artifacts.yml serves another
// forge's flow in a shape release images refuse, such as extract or one
// image repository per container; the harness then names it as
// artifacts-config, as the consumer's Forgejo release does.
const GitLabDedicatedReleaseImagesConfig = ".forge-ci/release-images.yml"

// GitLabReleaseImages reports whether a consumer's GitLab release builds
// release images, and the artifacts-config input that names their
// declaration: empty, the component's default, unless the consumer has a
// dedicated file.
//
// The default comes first: a consumer whose artifacts.yml declares containers
// releases them with an empty input. A dedicated file is admitted only beside
// an artifacts.yml that cannot be the release-image declaration; beside one
// that can, or one that declares no containers, the consumer has two
// declarations for one release and is refused.
func GitLabReleaseImages(consumerRoot string) (bool, string, error) {
	if consumerRoot == "" {
		return false, "", nil
	}

	defaultPath := filepath.Join(consumerRoot, DefaultArtifactsConfig)

	containers, err := declaredContainers(defaultPath)
	if err != nil {
		return false, "", err
	}

	dedicated, err := exists(filepath.Join(consumerRoot, GitLabDedicatedReleaseImagesConfig))
	if err != nil {
		return false, "", err
	}

	if !dedicated {
		return containers, "", nil
	}

	if !containers {
		return false, "", fmt.Errorf("%s declares no containers, so declare the release images there and delete %s: %w",
			DefaultArtifactsConfig, GitLabDedicatedReleaseImagesConfig, errs.ErrInvalidConfig)
	}

	if _, planErr := appconfig.ResolveReleaseImagesPlan(appconfig.EmitReleaseImagesPlanInput{
		Config: appconfig.EmitConfigPlanInput{Path: defaultPath},
		Images: pipeline.ReleaseImagesInput{
			ImageRepository: "registry.example/owner/app", Repository: "owner/app", ReleaseTag: "v0.0.0",
		},
	}, output.Annotator{}); planErr == nil {
		return false, "", fmt.Errorf("%s and %s both declare the release images; the default serves, so delete %s: %w",
			DefaultArtifactsConfig, GitLabDedicatedReleaseImagesConfig, GitLabDedicatedReleaseImagesConfig, errs.ErrInvalidConfig)
	}

	return true, GitLabDedicatedReleaseImagesConfig, nil
}

func declaredContainers(path string) (bool, error) {
	body, err := os.ReadFile(path) //nolint:gosec // G304: a fixed file under the consumer checkout the harness itself chose.
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	parsed, err := config.Parse(body)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}

	return len(parsed.Containers) != 0, nil
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	return err == nil, err
}
