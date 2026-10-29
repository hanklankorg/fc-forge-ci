// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPrebuiltMavenPublisherRefusesIncompleteHandoffWithoutRunningMaven(t *testing.T) {
	t.Parallel()
	deploy := jobStep(t, ".github/workflows/publish-maven-github.yml", "publish-maven-github", "Publish original Maven library build bytes")
	box, env := engineSandbox(t, "prebuilt-maven", map[string]string{"mvn": `#!/usr/bin/env bash
printf 'unexpected Maven execution' > "$FAKE_TOOL_LOG"
exit 99
`})
	box.write(".forge-ci/maven-build/pom.xml", `<project><groupId>org.example</groupId><artifactId>library</artifactId><version>1.2.3</version></project>`, 0o600)
	box.write(".forge-ci/maven-build/target/library-1.2.3.jar", "original built bytes", 0o600)

	got := box.run(deploy.Run, append(env, "VERSION=1.2.3", "MAVEN_SNAPSHOT=false")...)
	if got.status == 0 || !strings.Contains(got.stderr, "source/Javadoc") {
		t.Fatalf("incomplete handoff did not fail preflight: status=%d stderr=%s", got.status, got.stderr)
	}

	if _, err := os.Stat(filepath.Join(box.root, "tool-log")); !os.IsNotExist(err) {
		t.Fatalf("artifact-first publisher ran Maven: %v", err)
	}
}

func TestMavenPackagePublisherProfileThroughTheEngine(t *testing.T) {
	t.Parallel()

	deploy := jobStep(t, ".github/workflows/publish-maven-github.yml", "publish-maven-github", "Publish Maven to GitHub Packages")
	if deploy.Env["MAVEN_PROFILE"] != "${{ inputs.profile }}" {
		t.Fatal("package publisher does not bind its profile input to the CLI environment")
	}

	for _, profile := range []string{"", "library-attachments"} {
		t.Run("profile="+profile, func(t *testing.T) {
			t.Parallel()
			box, env := engineSandbox(t, "maven-package-profile", map[string]string{"mvn": `#!/usr/bin/env bash
set -eu
printf '%s\n' "$@" > "$FAKE_TOOL_LOG"
`})

			got := box.run(deploy.Run, append(env,
				"FORGEJO_ACTIONS=", "FORGE_CI_PROVIDER=github", "GITHUB_ACTIONS=true", "GITHUB_REPOSITORY=example/library",
				"GITHUB_SERVER_URL=https://github.com",
				"GITHUB_ACTOR=fixture", "GITHUB_TOKEN=fixture-token", "MAVEN_PROFILE="+profile,
				"MAVEN_CLI_OPTS="+deploy.Env["MAVEN_CLI_OPTS"])...)
			if got.status != 0 {
				t.Fatalf("package deploy failed: %d\n%s\n%s", got.status, got.stdout, got.stderr)
			}

			args := readSandboxFile(t, box, "tool-log")
			if strings.Contains(args, "\n-P") != (profile != "") || (profile != "" && !strings.Contains(args, "\n-P"+profile+"\n")) {
				t.Fatalf("publication profile %q not preserved in Maven args: %s", profile, args)
			}

			if !strings.Contains(args, "\ndeploy\n") || strings.Contains(args, "fixture-token") {
				t.Fatalf("expected Maven deploy without credential argv: %s", args)
			}
		})
	}
}

func TestMavenPackageStagesKeepTheLibraryBuildProfile(t *testing.T) {
	t.Parallel()

	for _, path := range []string{".github/workflows/release-publish-stage.yml", ".github/workflows/release-snapshot-publish-stage.yml"} {
		var workflow struct {
			Jobs map[string]struct {
				With map[string]any `yaml:"with"`
			} `yaml:"jobs"`
		}
		loadYAML(t, path, &workflow)

		want := "${{ matrix.artifact.project_type == 'maven' && matrix.artifact.build_type == 'library' && (matrix.artifact.maven.maven_profile || 'central-release') || '' }}"
		if got := workflow.Jobs["forge-packages"].With["profile"]; got != want {
			t.Fatalf("%s loses the library build profile: %v", path, got)
		}

		if got := workflow.Jobs["forge-packages"].With["from-build"]; got != "${{ matrix.artifact.project_type == 'maven' && matrix.artifact.build_type == 'library' }}" {
			t.Fatalf("%s must publish library build bytes rather than rebuild: %v", path, got)
		}
	}
}

func TestSnapshotSBOMLayerStepThroughTheEngine(t *testing.T) {
	t.Parallel()

	resolve := jobStep(t, ".github/workflows/release-snapshot-publish-stage.yml", "sbom", "Resolve artifact SBOM layers")
	for _, tc := range []struct {
		value, layers, present string
	}{
		{"build", "build", "true"},
		{"all", "build,analyzed-artifact", "true"},
		{"analyzed-container", "", "false"},
		{"none", "", "false"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			box, env := engineSandbox(t, "snapshot-sboms", nil)

			got := box.run(resolve.Run, append(env, "FORGEJO_ACTIONS=false", "FORGEJO_OUTPUT=", "GITHUB_ACTIONS=true", "GITHUB_OUTPUT="+box.outputs, "SBOMS="+tc.value)...)
			if got.status != 0 {
				t.Fatalf("snapshot layer step failed: %d\n%s\n%s", got.status, got.stdout, got.stderr)
			}

			outputs := box.output()
			for _, want := range []string{"layers=" + tc.layers + "\n", "has-layers=" + tc.present + "\n"} {
				if !strings.Contains(outputs, want) {
					t.Fatalf("snapshot outputs %q lack %q", outputs, want)
				}
			}
		})
	}
}

func TestPackagePublisherKeepsTheSelectedEngineRevision(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]struct {
			Uses  string         `yaml:"uses"`
			With  map[string]any `yaml:"with"`
			Needs []string       `yaml:"needs"`
		} `yaml:"jobs"`
	}
	loadYAML(t, ".github/workflows/release-publish-stage.yml", &workflow)

	publisher := workflow.Jobs["forge-packages"]
	if publisher.Uses != "./.github/workflows/publish-maven-github.yml" {
		t.Fatalf("unexpected package publisher %q", publisher.Uses)
	}

	resolver := workflow.Jobs["resolve-bootstrap"]
	if resolver.Uses != "./.github/workflows/resolve-forge-ci-bootstrap.yml" || resolver.With["forge-ci-binary-ref"] != "${{ inputs['forge-ci-binary-ref'] }}" {
		t.Fatalf("bootstrap must freeze the caller's selected engine revision: %+v", resolver)
	}
	if !slices.Contains(publisher.Needs, "resolve-bootstrap") {
		t.Fatal("package publisher must wait for the frozen bootstrap revision")
	}
	if got := publisher.With["forge-ci-binary-ref"]; got != "${{ needs.resolve-bootstrap.outputs.forge-ci-binary-ref }}" {
		t.Fatalf("package publisher loses the selected engine revision: %v", got)
	}
}
