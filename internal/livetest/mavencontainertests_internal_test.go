// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

func TestMavenContainerTestsStayInTheKeyFreeBuildJob(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), "test/live/demos/hello-maven/consumer/.forgejo/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	image := "docker.io/library/docker@sha256:" + strings.Repeat("b", 64)
	modified, err := mavenContainerTestWorkflow(string(body), image, "contract-privileged")
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := yaml.Unmarshal(body, &before); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(modified), &after); err != nil {
		t.Fatal(err)
	}
	beforeJobs := before["jobs"].(map[string]any)
	afterJobs := after["jobs"].(map[string]any)
	build := afterJobs["build"].(map[string]any)
	if !reflect.DeepEqual(build["runs-on"], []any{"contract-privileged"}) {
		t.Fatal("container tests did not select acknowledged capacity")
	}
	service := build["services"].(map[string]any)["docker"].(map[string]any)
	if service["image"] != image || !strings.Contains(service["options"].(string), "--privileged") || service["ports"] != nil || service["volumes"] != nil {
		t.Fatalf("test engine was not an isolated job-local sidecar: %v", service)
	}
	env := build["env"].(map[string]any)
	if env["DOCKER_HOST"] != "tcp://docker:2375" || env["TESTCONTAINERS_HOST_OVERRIDE"] != "docker" {
		t.Fatalf("test client not bound to private sidecar: %v", env)
	}
	delete(beforeJobs, "build")
	delete(afterJobs, "build")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("container-test infrastructure changed a signing, preparation or publication job")
	}
	for _, bad := range []string{"docker:latest", "docker@sha256:bad", "docker\ncommands@sha256:" + strings.Repeat("b", 64)} {
		if _, err := mavenContainerTestWorkflow(string(body), bad, "contract-privileged"); err == nil {
			t.Fatalf("accepted unpinned/injected engine %q", bad)
		}
	}
	if _, err := mavenContainerTestWorkflow(string(body), image, ""); err == nil {
		t.Fatal("accepted missing contract runner label")
	}
}

func TestMavenContainerTestOverlayPreservesOriginalSource(t *testing.T) {
	root, git := stageTestRepo(t, "sha1")
	pom := "<project><artifactId>wallet-account</artifactId><version>1.0.0</version></project>"
	stageTestWrite(t, root, "pom.xml", pom, 0644)
	stageTestWrite(t, root, "src/main/java/Original.java", "source-owned\n", 0644)
	stageTestCommit(t, git, root)
	source, err := openConsumer(t.Context(), git, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recipe := filepath.Join(reporoot.Path(t), "test/live/demos/hello-maven/consumer")
	fixture := NewMavenApplicationConsumerFixture(t, source, recipe, "registry.invalid/maven@sha256:"+strings.Repeat("a", 64))
	fixture = WithMavenContainerTests(t, fixture, "docker.io/library/docker@sha256:"+strings.Repeat("b", 64), "contract-privileged")
	for path, expected := range map[string]string{"pom.xml": pom, "src/main/java/Original.java": "source-owned\n"} {
		got, err := git.run(t.Context(), fixture.stage, "show", fixture.Commit+":"+path)
		if err != nil || got != strings.TrimSuffix(expected, "\n") {
			t.Fatalf("source blob %s changed: %q %v", path, got, err)
		}
	}
	if err := git.requireClean(t.Context(), root, "."); err != nil {
		t.Fatal("original checkout changed")
	}
}
