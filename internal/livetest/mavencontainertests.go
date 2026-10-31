// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// WithMavenContainerTests supplies a job-local test engine to the existing Maven
// recipe. Only the build job is changed; no host socket, signing job, source
// project file, or POM is rewritten. The controller admits privileged capacity.
func WithMavenContainerTests(tb TB, source Consumer, image string) Consumer {
	tb.Helper()
	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()
	const file = ".forgejo/workflows/release.yml"
	body, err := githubWorkflow(ctx, source, file)
	if err != nil {
		tb.Fatalf("Maven container-test workflow: %v", err)
	}
	workflow, err := mavenContainerTestWorkflow(body, image)
	if err != nil {
		tb.Fatalf("Maven container-test fixture: %v", err)
	}
	if err := source.stageFile(ctx, "100644", file, workflow); err != nil {
		tb.Fatalf("Maven container-test overlay: %v", err)
	}
	commit, err := source.commit(ctx, consumerBaseMessage)
	if err != nil {
		tb.Fatalf("Maven container-test commit: %v", err)
	}
	source.Commit = commit
	return source
}

func mavenContainerTestWorkflow(body, image string) (string, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9./:_-]+@sha256:[0-9a-f]{64}$`).MatchString(image) {
		return "", fmt.Errorf("test engine must be an operator-reviewed digest-pinned Docker service image")
	}
	var workflow map[string]any
	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		return "", err
	}
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("Maven recipe has no jobs")
	}
	build, ok := jobs["build"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("Maven recipe has no build job")
	}
	if _, ok := build["container"].(map[string]any); !ok {
		return "", fmt.Errorf("Maven container tests require an isolated job container")
	}
	if _, exists := build["services"]; exists {
		return "", fmt.Errorf("Maven recipe already declares build services")
	}
	env, ok := build["env"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("Maven recipe has no build environment")
	}
	for _, name := range []string{"DOCKER_HOST", "DOCKER_TLS_VERIFY", "TESTCONTAINERS_HOST_OVERRIDE"} {
		if _, exists := env[name]; exists {
			return "", fmt.Errorf("Maven recipe already owns %s", name)
		}
	}
	build["runs-on"] = "${{ fromJSON(vars.CI_CONTAINER_RUNNER_LABEL) }}"
	build["services"] = map[string]any{"docker": map[string]any{
		"image":   image,
		"env":     map[string]string{"DOCKER_TLS_CERTDIR": ""},
		"options": `--privileged --health-cmd "docker info" --health-interval 2s --health-timeout 5s --health-retries 60`,
	}}
	env["DOCKER_HOST"] = "tcp://docker:2375"
	env["DOCKER_TLS_VERIFY"] = ""
	env["TESTCONTAINERS_HOST_OVERRIDE"] = "docker"
	result, err := yaml.Marshal(workflow)
	return "# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government\n# SPDX-License-Identifier: CC0-1.0\n\n" + string(result), err
}
