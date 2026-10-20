// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
)

func writeMavenBuildFile(t *testing.T, root, name string, body []byte) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStageMavenBuildPreservesReactorPOMAndClassifierBytes(t *testing.T) {
	t.Parallel()
	for _, snapshot := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "snapshot"}[snapshot], func(t *testing.T) {
			t.Parallel()
			source, destination := t.TempDir(), t.TempDir()
			version := "1.2.3"
			if snapshot {
				version += "-SNAPSHOT"
			}
			parent := `<project><modelVersion>4.0.0</modelVersion><groupId>org.example</groupId><artifactId>parent</artifactId><version>` + version + `</version><packaging>pom</packaging><modules><module>library</module></modules></project>`
			child := `<project><modelVersion>4.0.0</modelVersion><parent><groupId>org.example</groupId><artifactId>parent</artifactId><version>` + version + `</version></parent><artifactId>library</artifactId><build><finalName>custom-${project.version}</finalName></build></project>`
			writeMavenBuildFile(t, source, "pom.xml", []byte(parent))
			writeMavenBuildFile(t, source, "library/pom.xml", []byte(child))
			for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar", "-tests.jar"} {
				writeMavenBuildFile(t, source, "library/target/custom-"+version+suffix, []byte("original build bytes: "+suffix))
			}
			if err := apppublish.StageMavenBuild(t.Context(), source, destination, version, snapshot); err != nil {
				t.Fatal(err)
			}
			for file, expected := range map[string][]byte{
				"org/example/parent/" + version + "/parent-" + version + ".pom":   []byte(parent),
				"org/example/library/" + version + "/library-" + version + ".pom": []byte(child),
			} {
				actual, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(file)))
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatalf("export changed POM bytes %s: %v", file, err)
				}
			}
			for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar", "-tests.jar"} {
				file := filepath.Join(destination, "org/example/library", version, "library-"+version+suffix)
				actual, err := os.ReadFile(file)
				if err != nil || string(actual) != "original build bytes: "+suffix {
					t.Fatalf("export changed %s: %v", suffix, err)
				}
				checksum, err := os.ReadFile(file + ".sha256")
				if err != nil || len(checksum) != 64 {
					t.Fatalf("publication checksum missing for %s: %v", suffix, err)
				}
			}
		})
	}
}

func TestStageMavenBuildRefusesIncompleteWrongVersionAndEscapingReactors(t *testing.T) {
	t.Parallel()
	for _, defect := range []string{"missing classifier", "wrong version", "module escape", "repeated module", "second XML root", "profile reactor"} {
		t.Run(defect, func(t *testing.T) {
			t.Parallel()
			source, destination := t.TempDir(), t.TempDir()
			pom := `<project><groupId>org.example</groupId><artifactId>library</artifactId><version>1.2.3</version></project>`
			if defect == "wrong version" {
				pom = strings.ReplaceAll(pom, "1.2.3", "1.2.2")
			}
			if defect == "second XML root" {
				pom += "<project/>"
			}
			if defect == "profile reactor" {
				pom = strings.ReplaceAll(pom, "</project>", "<profiles><profile><modules><module>untracked-build-output</module></modules></profile></profiles></project>")
			}
			if defect == "module escape" || defect == "repeated module" {
				module := "../outside"
				if defect == "repeated module" {
					module = "."
				}
				pom = strings.ReplaceAll(pom, "</project>", "<modules><module>"+module+"</module></modules></project>")
			}
			writeMavenBuildFile(t, source, "pom.xml", []byte(pom))
			for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar"} {
				if defect == "missing classifier" && suffix == "-sources.jar" {
					continue
				}
				writeMavenBuildFile(t, source, "target/library-1.2.3"+suffix, []byte("built"))
			}
			if err := apppublish.StageMavenBuild(t.Context(), source, destination, "1.2.3", false); err == nil {
				t.Fatal("invalid handoff accepted")
			}
		})
	}
}
