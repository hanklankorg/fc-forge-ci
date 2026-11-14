// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestMavenLibraryFixtureConfig(t *testing.T) {
	config := MavenLibraryFixtureConfig{RecipeDirectory: t.TempDir(), JavaImage: "registry.invalid/maven:21@sha256:" + strings.Repeat("a", 64), Profile: "central-release"}
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}

	for _, image := range []string{"maven:latest", "maven:21@sha256:bad", "maven:21\ncommands@sha256:" + strings.Repeat("a", 64)} {
		bad := config

		bad.JavaImage = image
		if bad.validate() == nil {
			t.Errorf("accepted unpinned or malformed image %q", image)
		}
	}

	config.Profile = "central-release -DskipTests"
	if config.validate() == nil {
		t.Fatal("accepted injected Maven profile")
	}
}

func TestMavenApplicationFixturePreservesAdmittedSource(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root, git := stageTestRepo(t, format)
			pom := "<project><artifactId>real-service</artifactId><version>0.2.1</version><build><finalName>custom-service</finalName></build></project>"
			stageTestWrite(t, root, "pom.xml", pom, 0644)
			stageTestWrite(t, root, "run-service", "#!/bin/sh\nexit 0\n", 0755)
			stageTestCommit(t, git, root)

			source, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			recipe := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-maven/consumer")

			image := "registry.invalid/maven:25@sha256:" + strings.Repeat("b", 64)
			fixture := NewMavenApplicationConsumerFixture(t, source, recipe, image)

			got, err := git.run(t.Context(), fixture.stage, "show", fixture.Commit+":pom.xml")
			if err != nil || got != pom {
				t.Fatalf("source POM changed: %q %v", got, err)
			}

			mode, err := git.run(t.Context(), fixture.stage, "ls-tree", fixture.Commit, "run-service")
			if err != nil || !strings.HasPrefix(mode, "100755") {
				t.Fatal("source executable mode changed")
			}

			workflow, err := git.run(t.Context(), fixture.stage, "show", fixture.Commit+":.forgejo/workflows/release.yml")
			if err != nil || !strings.Contains(workflow, "build-type: app") || !strings.Contains(workflow, "ARTIFACT_NAME: real-service") || !strings.Contains(workflow, "image: "+image) {
				t.Fatal("app recipe binding failed")
			}

			if !strings.Contains(workflow, mavenApplicationJarHandoff) {
				t.Fatal("app recipe still assumes Maven's default JAR filename")
			}

			if err := git.requireClean(t.Context(), root, "."); err != nil {
				t.Fatal("original source checkout modified")
			}
		})
	}
}

func TestMavenApplicationJarHandoff(t *testing.T) {
	for _, names := range [][]string{nil, {"custom-service.jar"}, {"service.jar", "other.jar"}, {"custom-service.jar", "original-custom-service.jar", "custom-service-sources.jar"}, {"real-service-4.0.1.jar", "real-service.jar"}} {
		t.Run(strings.Join(names, "+"), func(t *testing.T) {
			root := t.TempDir()
			for _, directory := range []string{"target", "dist"} {
				if err := os.Mkdir(filepath.Join(root, directory), 0700); err != nil {
					t.Fatal(err)
				}
			}

			for _, name := range names {
				if err := os.WriteFile(filepath.Join(root, "target", name), []byte("original jar bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}

			command := exec.CommandContext(t.Context(), "bash", "-euo", "pipefail", "-c", "ARTIFACT_NAME=real-service; RELEASE_TAG=v4.0.1; "+mavenApplicationJarHandoff)
			command.Dir = root

			output, err := command.CombinedOutput()
			if len(names) == 2 && names[0] == "real-service-4.0.1.jar" {
				if err != nil {
					t.Fatalf("handoff refused an assembly beside the versioned JAR: %v: %s", err, output)
				}

				for _, name := range names {
					if got, readErr := os.ReadFile(filepath.Join(root, "dist", name)); readErr != nil || string(got) != "original jar bytes" {
						t.Fatalf("%s not shipped unchanged: %q, %v", name, got, readErr)
					}
				}

				return
			}

			if len(names) != 1 && len(names) != 3 {
				if err == nil {
					t.Fatal("accepted missing or ambiguous application JAR")
				}

				return
			}

			if err != nil {
				t.Fatalf("handoff: %v: %s", err, output)
			}

			got, err := os.ReadFile(filepath.Join(root, "dist", "real-service-4.0.1.jar"))
			if err != nil || string(got) != "original jar bytes" {
				t.Fatalf("release JAR bytes changed: %q, %v", got, err)
			}
		})
	}
}

func TestMavenLibraryFixtureAdmittedPOM(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root, git := stageTestRepo(t, format)
			stageTestWrite(t, root, "pom.xml", "<project><artifactId>admitted-library</artifactId></project>", 0644)
			stageTestCommit(t, git, root)

			source, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// A later worktree replacement must not change admitted identity.
			stageTestWrite(t, root, "pom.xml", "<project><artifactId>later-worktree</artifactId></project>", 0644)

			artifact, err := mavenLibraryArtifact(t.Context(), source)
			if err != nil || artifact != "admitted-library" {
				t.Fatalf("admitted artifact = %q, %v", artifact, err)
			}
		})
	}

	t.Run("reject tracked symlink", func(t *testing.T) {
		root, git := stageTestRepo(t, "sha1")
		stageTestWrite(t, root, "actual.xml", "<project><artifactId>linked-library</artifactId></project>", 0644)

		if err := os.Symlink("actual.xml", filepath.Join(root, "pom.xml")); err != nil {
			t.Fatal(err)
		}

		stageTestCommit(t, git, root)

		source, err := openConsumer(t.Context(), git, root, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		if _, err = mavenLibraryArtifact(t.Context(), source); err == nil {
			t.Fatal("admitted a symlink POM")
		}
	})

	// The fixture owns one JAR's handoff: a parent POM is not counted as one.
	t.Run("reject aggregator", func(t *testing.T) {
		root, git := stageTestRepo(t, "sha1")
		stageTestWrite(t, root, "pom.xml", "<project><artifactId>parent</artifactId><packaging>pom</packaging><modules><module>library</module></modules></project>", 0644)
		stageTestCommit(t, git, root)

		source, err := openConsumer(t.Context(), git, root, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		if _, err = mavenLibraryArtifact(t.Context(), source); !errors.Is(err, errSingleModuleJarLibrary) {
			t.Fatalf("admitted an aggregator: %v", err)
		}
	})
}
