// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	errTestEngineImage       = errors.New("test engine must be an operator-reviewed digest-pinned Docker service image")
	errTestEngineRunnerLabel = errors.New("test engine requires the acknowledged contract runner label")
)

//nolint:staticcheck // ST1005: Maven is a proper noun and the message text is asserted.
var (
	//lint:ignore ST1005 Maven is a proper noun and the message text is asserted.
	errMavenRecipeNoJobs = errors.New("Maven recipe has no jobs")
	//lint:ignore ST1005 Maven is a proper noun and the message text is asserted.
	errMavenRecipeNoBuildJob = errors.New("Maven recipe has no build job")
	//lint:ignore ST1005 Maven is a proper noun and the message text is asserted.
	errMavenNoJobContainer = errors.New("Maven container tests require an isolated job container")
	//lint:ignore ST1005 Maven is a proper noun and the message text is asserted.
	errMavenRecipeHasServices = errors.New("Maven recipe already declares build services")
	//lint:ignore ST1005 Maven is a proper noun and the message text is asserted.
	errMavenRecipeNoBuildEnv = errors.New("Maven recipe has no build environment")
	//lint:ignore ST1005 Maven is a proper noun and the message text is asserted.
	errMavenRecipeOwnsVariable = errors.New("Maven recipe already owns")
)

// WithMavenContainerTests supplies a job-local test engine to the existing Maven
// recipe. Only the build job is changed; no host socket, signing job, source
// project file, or POM is rewritten. The controller admits privileged capacity.
func WithMavenContainerTests(tb TB, source Consumer, image, runnerLabel string) Consumer {
	tb.Helper()

	return restageForgejoRelease(tb, source, "Maven container-test", func(body string) (string, error) {
		return mavenContainerTestWorkflow(body, image, runnerLabel)
	})
}

func mavenContainerTestWorkflow(body, image, runnerLabel string) (string, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9./:_-]+@sha256:[0-9a-f]{64}$`).MatchString(image) {
		return "", errTestEngineImage
	}

	if runnerLabel == "" || strings.ContainsAny(runnerLabel, "\r\n") {
		return "", errTestEngineRunnerLabel
	}

	workflow, jobs, err := forgejoWorkflowJobs(body)
	if err != nil {
		return "", err
	}

	if jobs == nil {
		return "", errMavenRecipeNoJobs
	}

	build, ok := jobs["build"].(map[string]any)
	if !ok {
		return "", errMavenRecipeNoBuildJob
	}

	if _, ok := build["container"].(map[string]any); !ok {
		return "", errMavenNoJobContainer
	}

	if _, exists := build["services"]; exists {
		return "", errMavenRecipeHasServices
	}

	env, ok := build[fieldEnv].(map[string]any)
	if !ok {
		return "", errMavenRecipeNoBuildEnv
	}

	for _, name := range []string{"DOCKER_HOST", "DOCKER_TLS_VERIFY", "TESTCONTAINERS_HOST_OVERRIDE"} {
		if _, exists := env[name]; exists {
			return "", fmt.Errorf("%w %s", errMavenRecipeOwnsVariable, name)
		}
	}

	build["runs-on"] = []string{runnerLabel}
	build["services"] = map[string]any{"docker": map[string]any{
		"image":   image,
		fieldEnv:  map[string]string{"DOCKER_TLS_CERTDIR": ""},
		"options": `--privileged --health-cmd "docker info" --health-interval 2s --health-timeout 5s --health-retries 60`,
	}}
	env["DOCKER_HOST"] = "tcp://docker:2375"
	env["DOCKER_TLS_VERIFY"] = ""
	env["TESTCONTAINERS_HOST_OVERRIDE"] = "docker"

	return encodeForgejoFixture(workflow)
}
