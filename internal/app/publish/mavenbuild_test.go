// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

			parent := `<project><modelVersion>4.0.0</modelVersion><groupId>org.example</groupId><artifactId>parent</artifactId><version>` + version + `</version><packaging>pom</packaging><modules><module>library</module><module>application</module></modules></project>`
			child := `<project><modelVersion>4.0.0</modelVersion><parent><groupId>org.example</groupId><artifactId>parent</artifactId><version>` + version + `</version></parent><artifactId>library</artifactId><build><finalName>custom-${project.version}</finalName></build></project>`
			// The downstream module inherits its version and depends on its sibling,
			// not on an unrelated external artifact. Export must retain this model
			// byte-for-byte; this offline handoff test does not execute Maven.
			application := `<project><modelVersion>4.0.0</modelVersion><parent><groupId>org.example</groupId><artifactId>parent</artifactId><version>` + version + `</version></parent><artifactId>application</artifactId><dependencies><dependency><groupId>org.example</groupId><artifactId>library</artifactId><version>${project.version}</version></dependency></dependencies></project>`

			writeMavenBuildFile(t, source, "pom.xml", []byte(parent))
			writeMavenBuildFile(t, source, "library/pom.xml", []byte(child))
			writeMavenBuildFile(t, source, "application/pom.xml", []byte(application))

			for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar", "-tests.jar"} {
				writeMavenBuildFile(t, source, "library/target/custom-"+version+suffix, []byte("original build bytes: library"+suffix))
				writeMavenBuildFile(t, source, "application/target/application-"+version+suffix, []byte("original build bytes: application"+suffix))
			}

			if err := apppublish.StageMavenBuild(t.Context(), source, destination, version, snapshot); err != nil {
				t.Fatal(err)
			}

			payloads := map[string][]byte{
				"org/example/parent/" + version + "/parent-" + version + ".pom":           []byte(parent),
				"org/example/library/" + version + "/library-" + version + ".pom":         []byte(child),
				"org/example/application/" + version + "/application-" + version + ".pom": []byte(application),
			}

			for _, artifact := range []string{"library", "application"} {
				for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar", "-tests.jar"} {
					file := "org/example/" + artifact + "/" + version + "/" + artifact + "-" + version + suffix
					payloads[file] = []byte("original build bytes: " + artifact + suffix)
				}
			}

			expectedPayloads := make([]string, 0, len(payloads))
			for file, expected := range payloads {
				expectedPayloads = append(expectedPayloads, file)

				actual, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(file)))
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatalf("export changed %s: %v", file, err)
				}

				checksum, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(file+".sha256")))
				if err != nil || string(checksum) != fmt.Sprintf("%x", sha256.Sum256(expected)) {
					t.Fatalf("publication checksum does not bind %s: %v", file, err)
				}
			}

			var set apppublish.MavenPublicationSet

			var err error

			if snapshot {
				set, err = apppublish.InspectMavenSnapshot(destination, version)
			} else {
				set, err = apppublish.InspectMavenPublication(destination, version, false)
			}

			if err != nil {
				t.Fatal(err)
			}

			slices.Sort(expectedPayloads)

			if len(set.Publications) != 3 || !slices.Equal(set.Payloads, expectedPayloads) {
				t.Fatalf("reactor publication inventory: %+v", set)
			}

			// A complete first child must not mask an incomplete downstream child.
			for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar"} {
				file := filepath.Join(source, "application/target/application-"+version+suffix)
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}

				if err := apppublish.StageMavenBuild(t.Context(), source, t.TempDir(), version, snapshot); err == nil {
					t.Fatalf("accepted downstream module missing %s", suffix)
				}

				writeMavenBuildFile(t, source, "application/target/application-"+version+suffix, []byte("original build bytes: application"+suffix))
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
