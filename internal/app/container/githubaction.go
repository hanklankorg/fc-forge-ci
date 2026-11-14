// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// GitHubKanikoActionDirectory is the local action's fixed, workspace-relative
// coordinate. Consumer source lives in source/, outside this control directory.
const GitHubKanikoActionDirectory = ".forge-ci-kaniko-action"

// GitHubKanikoOutputDirectory is shared by the container action and host steps.
const GitHubKanikoOutputDirectory = ".forge-ci-kaniko-output"

//go:embed github-kaniko-action.sh
var githubKanikoEntrypoint []byte

// GitHubKanikoAction contains already-resolved native tools and a validated
// build plan. No registry credentials are written into the generated image.
type GitHubKanikoAction struct {
	Workspace, JobImage, Engine, Scanner string
	Plan                                 []byte
	Snapshot                             bool
}

// PrepareGitHubKanikoAction packages the existing CLI in a disposable Docker
// action. GitHub owns container execution; the Go CLI still owns the build and
// scan. Source, control files and output have separate workspace coordinates.
func PrepareGitHubKanikoAction(in GitHubKanikoAction) (string, error) {
	if !domaincontainer.ValidDigestReference(in.JobImage) {
		return "", fmt.Errorf("kaniko job image must be a fully qualified SHA-256 digest reference: %w", errs.ErrValidation)
	}

	root, err := os.OpenRoot(in.Workspace)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()

	source, err := root.Lstat("source")
	if err != nil || !source.IsDir() {
		return "", fmt.Errorf("kaniko action requires consumer source in the workspace's source/ directory: %w", errs.ErrValidation)
	}

	output := GitHubKanikoOutputDirectory
	if in.Snapshot {
		output = "source/snapshot-layouts"
	}

	if _, err = root.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("kaniko action output must not already exist: %w", errs.ErrValidation)
	}

	if err = root.Mkdir(GitHubKanikoActionDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create fresh kaniko action directory: %w", err)
	}

	complete := false
	defer func() {
		if !complete {
			_ = root.RemoveAll(GitHubKanikoActionDirectory)
		}
	}()

	action, err := root.OpenRoot(GitHubKanikoActionDirectory)
	if err != nil {
		return "", err
	}
	defer func() { _ = action.Close() }()

	if err = writeGitHubKanikoAction(action, in); err != nil {
		return "", err
	}

	complete = true

	return filepath.Join(in.Workspace, output), nil
}

func writeGitHubKanikoAction(action *os.Root, in GitHubKanikoAction) error {
	dockerfile := "FROM " + in.JobImage + "\nCOPY forge-ci entrypoint.sh plan.json /kaniko/tools/\n"

	payloads := map[string]string{"forge-ci": in.Engine}
	if in.Scanner != "" {
		payloads["trivy"] = in.Scanner
		dockerfile += "COPY trivy /kaniko/tools/trivy\n"
	}

	for name, path := range payloads {
		body, readErr := os.ReadFile(path) //nolint:gosec // native executable resolved by the CLI, not consumer source.
		if readErr != nil {
			return fmt.Errorf("read native %s: %w", name, readErr)
		}

		if err := action.WriteFile(name, body, 0o755); err != nil {
			return err
		}
	}

	for name, body := range map[string][]byte{
		"action.yml": []byte(githubKanikoActionMetadata),
		"Dockerfile": []byte(dockerfile), "plan.json": in.Plan,
	} {
		if err := action.WriteFile(name, body, 0o600); err != nil {
			return err
		}
	}

	return action.WriteFile("entrypoint.sh", githubKanikoEntrypoint, 0o755)
}

const githubKanikoActionMetadata = `name: Kaniko native build
description: Run the existing forge-ci build in an isolated Kaniko container.
inputs:
  operation:
    default: image
    description: image, extract or snapshot
runs:
  using: docker
  image: Dockerfile
  entrypoint: /kaniko/tools/entrypoint.sh
  args:
    - ${{ inputs.operation }}
`
