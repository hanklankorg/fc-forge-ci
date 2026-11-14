// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	_ "embed"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Shared with the live owner so ordinary offline tests check the actual graph.
//
//go:embed testdata/layer/minimal-binary-release.yml
var layerMinimalReleaseFixture string

const (
	// releaseWorkflowFile is where the rendered fixture is committed. The
	// fixture names the same file as its caller-workflow.
	releaseWorkflowFile = ".forgejo/workflows/layer-minimal-release.yml"

	// layerDeclarationFile is where the release reads a consumer's
	// declaration from by default.
	layerDeclarationFile = ".forge-ci/artifacts.yml"

	releaseBuildJob = "Build minimal binary release"
)

// The adverse release cases rest on the fixture's shape, not on what a run
// happened to do: the tests run in the build job, before anything is handed
// over, and the publisher starts only for a build that succeeded. Its
// always() makes the condition, not needs alone, what holds it back.
func TestMinimalReleaseFixtureGatesPublicationOnTheBuild(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Needs yaml.Node         `yaml:"needs"`
			If    string            `yaml:"if"`
			Uses  string            `yaml:"uses"`
			With  map[string]string `yaml:"with"`
			Steps []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(layerMinimalReleaseFixture), &workflow); err != nil {
		t.Fatal(err)
	}

	needs := func(job string) []string {
		node := workflow.Jobs[job].Needs
		if node.Kind == yaml.ScalarNode {
			return []string{node.Value}
		}

		names := make([]string, 0, len(node.Content))
		for _, need := range node.Content {
			names = append(names, need.Value)
		}

		return names
	}

	if got := needs("build"); len(got) != 1 || got[0] != "prepare" {
		t.Fatalf("the build must follow the prepared release alone, got %v", got)
	}

	built, uploaded := -1, -1

	for index, step := range workflow.Jobs["build"].Steps {
		switch {
		case strings.Contains(step.Uses, "/.forgejo/actions/build-go@"):
			if _, set := step.With["enforce-application-tests"]; set || built != -1 {
				t.Fatalf("the build must call build-go once and leave the administrator's input to the renderer: %+v", step)
			}

			built = index
		case strings.Contains(step.Uses, "/.forgejo/actions/upload-dist@"):
			uploaded = index
		}
	}

	if built == -1 || uploaded <= built {
		t.Fatalf("the layer's Go build (step %d) must run before the dist hand-off (step %d)", built, uploaded)
	}

	publisher := workflow.Jobs["sign-and-publish"]
	if got := needs("sign-and-publish"); len(got) != 2 || got[0] != "prepare" || got[1] != "build" ||
		publisher.If != "${{ always() && needs.build.result == 'success' }}" ||
		!strings.Contains(publisher.Uses, "/.forgejo/workflows/sign-and-publish-release.yml@") ||
		publisher.With["dist-digest"] != "${{ needs.build.outputs.dist-digest }}" {
		t.Fatalf("the publisher must need a successful build and take its hand-off by digest, got needs %v, if %q, with %v", got, publisher.If, publisher.With)
	}
}
