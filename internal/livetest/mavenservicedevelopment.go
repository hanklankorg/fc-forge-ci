// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

// MavenServiceDevelopmentConfig supplies trusted trial infrastructure. Fixture
// is a CI-side script producing trial-runtime/runtime.env and optional files;
// it runs in the Java job, never on the controller or in the source checkout.
type MavenServiceDevelopmentConfig struct {
	RecipeDirectory, JavaImage, Branch, ReadinessURL, Fixture string
}

// MavenServiceDevelopmentVariables writes shared bindings once and refuses a
// caller plan that would change the operator-supplied repository authority.
func MavenServiceDevelopmentVariables(plan, runtime map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(plan)+len(runtime))
	for key, value := range plan {
		result[key] = value
	}
	for key, value := range runtime {
		if previous, found := result[key]; found && previous != value {
			return nil, fmt.Errorf("conflicting service variable %s", key)
		}
		result[key] = value
	}
	return result, nil
}

//go:embed testdata/maven-service-development.yml
var mavenServiceDevelopmentRecipe string

// NewMavenServiceDevelopmentFixture reuses the native Maven and snapshot layer
// recipes, overlaying CI only in Consumer's private index. The original
// Containerfile builds independently; checked JAR/image byte equality is NOT claimed.
func NewMavenServiceDevelopmentFixture(tb TB, source Consumer, config MavenServiceDevelopmentConfig) Consumer {
	tb.Helper()
	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()
	fixture, err := mavenServiceDevelopmentFixture(ctx, source, config)
	if err != nil {
		tb.Fatalf("Maven service development fixture: %v", err)
	}
	return fixture
}

func mavenServiceDevelopmentFixture(ctx context.Context, source Consumer, c MavenServiceDevelopmentConfig) (Consumer, error) {
	if !filepath.IsAbs(c.RecipeDirectory) || !container.ValidDigestReference(c.JavaImage) {
		return source, fmt.Errorf("name an absolute native recipe directory and a full digest-pinned Java job image")
	}
	endpoint, err := url.Parse(c.ReadinessURL)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.Path == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return source, fmt.Errorf("readiness must be an explicit http://127.0.0.1:<port>/<project-endpoint> URL")
	}
	if port, err := strconv.Atoi(endpoint.Port()); err != nil || port < 1 || port > 65535 {
		return source, fmt.Errorf("readiness requires a valid container port")
	}
	if _, err = source.git.run(ctx, source.stage, "check-ref-format", "refs/heads/"+c.Branch); err != nil || c.Branch == "main" || c.Branch == "" {
		return source, fmt.Errorf("select a valid source branch different from policy main")
	}
	artifact, err := mavenLibraryArtifact(ctx, source)
	if err != nil {
		return source, err
	}
	if _, err = githubWorkflow(ctx, source, "Containerfile"); err != nil {
		return source, err
	}
	var fixture string
	if c.Fixture != "" {
		if !filepath.IsAbs(c.Fixture) {
			return source, fmt.Errorf("fixture must be an absolute CI script path")
		}
		info, err := os.Lstat(c.Fixture)
		if err != nil || !info.Mode().IsRegular() {
			return source, fmt.Errorf("fixture must be a regular CI script")
		}
		body, err := os.ReadFile(c.Fixture)
		if err != nil {
			return source, err
		}
		fixture = string(body)
	}
	body, err := os.ReadFile(filepath.Join(c.RecipeDirectory, ".forgejo/workflows/release.yml"))
	if err != nil {
		return source, err
	}
	pin, err := ReadConsumerPin(map[string]string{"release.yml": string(body)})
	if err != nil {
		return source, err
	}
	// YAML quoting prevents branch/endpoint metadata becoming syntax.
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	workflow := strings.NewReplacer("@ACTIONS@", pin.Actions, "@WORKFLOWS@", pin.Workflows,
		"@PIN@", pin.Commit, "@IMAGE@", c.JavaImage, "@BRANCH@", quote(c.Branch),
		"@URL@", quote(c.ReadinessURL), "@ARTIFACT@", artifact).Replace(mavenServiceDevelopmentRecipe)
	if _, err = source.git.run(ctx, source.stage, "read-tree", source.Commit); err != nil {
		return source, err
	}
	// A container-only declaration deliberately creates no unused Maven producer.
	// Maven's original checked app lifecycle runs once in the separate check job.
	declaration := "containers:\n  - name: service\n    container-file: Containerfile\n    context: .\n    platforms: linux/amd64\n    enable-scan: false\n    enable-slsa: false\n"
	for path, text := range map[string]string{
		".forgejo/workflows/development.yml": workflow,
		".forge-ci/service-development.yml":  declaration,
		".forge-ci/service-fixture.sh":       fixture,
	} {
		if err = source.stageFile(ctx, "100644", path, text); err != nil {
			return source, err
		}
	}
	source.Commit, err = source.commit(ctx, consumerBaseMessage)
	return source, err
}

// DispatchConsumerDevelopment dispatches policy main once; selected source is
// resolved by the shared snapshot planner, not by this controller.
func DispatchConsumerDevelopment(tb TB, target Target, repo string) {
	tb.Helper()
	requireAccepted(tb, target)
	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(repo) {
		tb.Fatalf("invalid scratch repository")
	}
	err := discard(ctx, target, http.MethodPost, actionsURL(target, repo, "/actions/workflows/development.yml/dispatches"), map[string]string{"ref": "main"}, http.StatusNoContent)
	if err != nil {
		tb.Fatalf("dispatch development policy: %v", err)
	}
}
