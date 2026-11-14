// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	errRecipeDirectoryRelative = errors.New("recipe directory must be absolute")
	errJavaImageUnpinned       = errors.New("JavaImage must be a digest-pinned Maven/JDK 21 image")
	errLibraryProfileRequired  = errors.New("a project-owned library profile is required")
	errSingleModuleJarLibrary  = errors.New("fixture requires a single-module jar library with a literal artifactId")
)

// MavenLibraryFixtureConfig supplies runtime infrastructure and the existing
// native Maven consumer recipe. No source project or signing key is rewritten.
type MavenLibraryFixtureConfig struct {
	RecipeDirectory string
	// JavaImage must be an operator-supplied Maven/JDK 21 image pinned by digest.
	JavaImage string
	Profile   string
}

// NewMavenApplicationConsumerFixture binds the existing Maven app recipe to a
// real single-module application, changing CI files only in the private index.
func NewMavenApplicationConsumerFixture(tb TB, source Consumer, recipe, image string) Consumer {
	tb.Helper()

	config := MavenLibraryFixtureConfig{RecipeDirectory: recipe, JavaImage: image, Profile: "default"}
	if err := config.validate(); err != nil {
		tb.Fatalf("Maven app fixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	artifact, err := mavenLibraryArtifact(ctx, source)
	if err != nil {
		tb.Fatalf("Maven app identity: %v", err)
	}

	for _, file := range mavenConsumerRecipeFiles {
		body, err := os.ReadFile(filepath.Join(recipe, file)) //nolint:gosec // operator-supplied recipe directory joined with a fixed recipe file name.
		if err != nil {
			tb.Fatalf("Maven app recipe: %v", err)
		}

		text := string(body)
		if strings.HasSuffix(file, "/release.yml") {
			text = regexp.MustCompile(`(?m)^(      image: |      runtime-image-java: ).*$`).ReplaceAllString(text, "${1}"+image)
			text = strings.ReplaceAll(text, "ARTIFACT_NAME: hello-maven", "ARTIFACT_NAME: "+artifact)
			// An application may own build.finalName (for example an unversioned
			// Spring Boot JAR). Keep its POM intact and require one built JAR,
			// rather than guessing its filename or choosing an ambiguous output.
			text = strings.Replace(text, `          cp -- "target/${ARTIFACT_NAME}-${RELEASE_TAG#v}.jar" dist/`, mavenApplicationJarHandoff, 1)
		}

		if err := source.stageFile(ctx, "100644", file, text); err != nil {
			tb.Fatalf("Maven app overlay: %v", err)
		}
	}

	if err := stageMavenRecipeLicenses(ctx, source, recipe); err != nil {
		tb.Fatalf("Maven app recipe licensing: %v", err)
	}

	commit, err := source.commit(ctx, consumerBaseMessage)
	if err != nil {
		tb.Fatalf("Maven app fixture commit: %v", err)
	}

	source.Commit = commit

	return source
}

// The handoff skips the JARs the engine's release assembly never ships
// (original-*, sources, Javadoc, tests). Several remaining JARs are accepted
// only beside the versioned main JAR, as when an assembly writes an
// unversioned executable JAR; all of them ship, as the engine would ship them.
const mavenApplicationJarHandoff = `          shopt -s nullglob
          jars=()
          for jar in target/*.jar; do
            case "${jar##*/}" in original-*|*-sources.jar|*-javadoc.jar|*-tests.jar) continue ;; esac
            jars+=("$jar")
          done
          versioned="target/${ARTIFACT_NAME}-${RELEASE_TAG#v}.jar"
          if [ "${#jars[@]}" -eq 1 ]; then
            cp -- "${jars[0]}" "dist/${ARTIFACT_NAME}-${RELEASE_TAG#v}.jar"
          elif [ "${#jars[@]}" -gt 1 ] && [ -f "$versioned" ]; then
            cp -- "${jars[@]}" dist/
          else
            echo "Maven application fixture requires one target JAR, or several beside ${versioned}; found ${#jars[@]}" >&2
            exit 1
          fi`

func (config MavenLibraryFixtureConfig) validate() error {
	if !filepath.IsAbs(config.RecipeDirectory) {
		return errRecipeDirectoryRelative
	}

	if !regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[0-9a-f]{64}$`).MatchString(config.JavaImage) {
		return errJavaImageUnpinned
	}

	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`).MatchString(config.Profile) {
		return errLibraryProfileRequired
	}

	return nil
}

// mavenLibraryArtifact reads identity from the admitted revision's regular POM
// blob. It never follows a worktree symlink or rereads a mutable worktree POM.
func mavenLibraryArtifact(ctx context.Context, source Consumer) (string, error) {
	// githubWorkflow validates exact path and mode 100644/100755 before reading
	// the blob from source.Commit; its validation also applies to non-workflows.
	pom, err := githubWorkflow(ctx, source, "pom.xml")
	if err != nil {
		return "", err
	}

	var model struct {
		Artifact  string   `xml:"artifactId"`
		Packaging string   `xml:"packaging"`
		Modules   []string `xml:"modules>module"`
	}
	if err = xml.Unmarshal([]byte(pom), &model); err != nil {
		return "", err
	}

	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(model.Artifact) || (model.Packaging != "" && model.Packaging != mavenPackagingJar) || len(model.Modules) != 0 {
		return "", errSingleModuleJarLibrary
	}

	return model.Artifact, nil
}
