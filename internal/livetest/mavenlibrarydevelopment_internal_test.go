// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later
package livetest

import (
	"gopkg.in/yaml.v3"
	"os/exec"
	"strings"
	"testing"
)

func TestMavenLibraryDevelopmentContract(t *testing.T) {
	for _, version := range []string{"1.2.3", "${revision}", "1.2.3-SNAPSHOT"} {
		_, err := parseMavenDevelopmentPOM("<project><groupId>org.example</groupId><artifactId>library</artifactId><version>" + version + "</version></project>")
		if (err == nil) != strings.HasSuffix(version, "-SNAPSHOT") {
			t.Fatalf("snapshot version contract %s: %v", version, err)
		}
	}
	body := mavenDevelopmentWorkflow(MavenLibraryFixtureConfig{JavaImage: "registry.invalid/java@sha256:" + strings.Repeat("a", 64), Profile: "central-release"}, ConsumerPin{Actions: "https://forge.invalid/owner/engine/.forgejo/actions/", Commit: strings.Repeat("a", 40)}, MavenLibraryDevelopment{"org.example", "library", "1.2.3-SNAPSHOT"}, "Oracle")
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow.Jobs) != 3 {
		t.Fatal("expected separate producer, publisher and consumer")
	}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Run != "" {
				cmd := exec.Command("bash", "-n")
				cmd.Stdin = strings.NewReader(step.Run)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("shell: %v %s", err, out)
				}
			}
		}
	}
	if strings.Contains(body, "RELEASE_") || strings.Contains(body, "skip-tests") || strings.Contains(body, "download-dist") {
		t.Fatal("invalid development authority/build contract")
	}
	for _, want := range []string{"branches: [development]", "--from-build --snapshot", "-Dmaven.repo.local=", "LIBRARY_REGISTRY_ORACLE_OK:", "build-type: lib", "profile: central-release"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
