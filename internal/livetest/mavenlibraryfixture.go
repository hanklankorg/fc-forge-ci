// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
		body, err := os.ReadFile(filepath.Join(recipe, file))
		if err != nil {
			tb.Fatalf("Maven app recipe: %v", err)
		}
		text := string(body)
		if strings.HasSuffix(file, "/release.yml") {
			text = regexp.MustCompile(`(?m)^(      image: |      version-bump-image: ).*$`).ReplaceAllString(text, "${1}"+image)
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

const mavenApplicationJarHandoff = `          shopt -s nullglob
          jars=(target/*.jar)
          if [ "${#jars[@]}" -ne 1 ]; then
            echo "Maven application fixture requires exactly one target JAR; found ${#jars[@]}" >&2
            exit 1
          fi
          cp -- "${jars[0]}" "dist/${ARTIFACT_NAME}-${RELEASE_TAG#v}.jar"`

func (config MavenLibraryFixtureConfig) validate() error {
	if !filepath.IsAbs(config.RecipeDirectory) {
		return fmt.Errorf("recipe directory must be absolute")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[0-9a-f]{64}$`).MatchString(config.JavaImage) {
		return fmt.Errorf("JavaImage must be a digest-pinned Maven/JDK 21 image")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`).MatchString(config.Profile) {
		return fmt.Errorf("a project-owned library profile is required")
	}
	return nil
}

// NewMavenLibraryConsumerFixture overlays only the private index opened by
// OpenConsumer. The returned consumer uses the existing PlanConsumer/Push
// protocol. Its source checkout and source objects remain read-only. This
// preparer does not execute Maven or contact a forge.
//
// The recipe directory is test/live/demos/hello-maven/consumer in the engine
// checkout. Existing changelog inputs are copied from that recipe; application
// source, POM, rules and checks come exclusively from the admitted consumer.
func NewMavenLibraryConsumerFixture(tb TB, source Consumer, config MavenLibraryFixtureConfig) Consumer {
	tb.Helper()
	if err := config.validate(); err != nil {
		tb.Fatalf("Maven library fixture: %v", err)
		return Consumer{}
	}
	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()
	fixture, err := newMavenLibraryConsumerFixture(ctx, source, config)
	if err != nil {
		tb.Fatalf("Maven library fixture: %v", err)
	}
	return fixture
}

func newMavenLibraryConsumerFixture(ctx context.Context, source Consumer, config MavenLibraryFixtureConfig) (Consumer, error) {
	if err := config.validate(); err != nil {
		return Consumer{}, err
	}
	artifact, err := mavenLibraryArtifact(ctx, source)
	if err != nil {
		return Consumer{}, err
	}
	if _, err = source.git.run(ctx, source.stage, "read-tree", source.Commit); err != nil {
		return Consumer{}, err
	}
	for _, file := range mavenConsumerRecipeFiles {
		body, readErr := os.ReadFile(filepath.Join(config.RecipeDirectory, file))
		if readErr != nil {
			return Consumer{}, readErr
		}
		text := string(body)
		if strings.HasPrefix(file, ".forgejo/") {
			text, err = mavenLibraryWorkflow(text, file, artifact, config)
			if err != nil {
				return Consumer{}, err
			}
		}
		if err = source.stageFile(ctx, "100644", file, text); err != nil {
			return Consumer{}, err
		}
	}
	if err = stageMavenRecipeLicenses(ctx, source, config.RecipeDirectory); err != nil {
		return Consumer{}, err
	}
	commit, err := source.commit(ctx, consumerBaseMessage)
	if err != nil {
		return Consumer{}, err
	}
	source.Commit = commit
	return source, nil
}

// MavenLibraryArtifact reads identity from the admitted revision's regular POM
// blob. It never follows a worktree symlink or rereads a mutable worktree POM.
func MavenLibraryArtifact(tb TB, source Consumer) string {
	tb.Helper()
	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()
	artifact, err := mavenLibraryArtifact(ctx, source)
	if err != nil {
		tb.Fatalf("Maven library POM: %v", err)
	}
	return artifact
}

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
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(model.Artifact) || (model.Packaging != "" && model.Packaging != "jar") || len(model.Modules) != 0 {
		return "", fmt.Errorf("fixture requires a single-module jar library with a literal artifactId")
	}
	return model.Artifact, nil
}

func mavenLibraryWorkflow(text, file, artifact string, config MavenLibraryFixtureConfig) (string, error) {
	if !strings.Contains(text, "build-type: app") {
		return "", fmt.Errorf("native Maven recipe changed: %s has no build-type", file)
	}
	text = strings.ReplaceAll(text, "build-type: app", "build-type: lib\n          profile: "+config.Profile)
	// The lint job retains the independently supplied nanolinter image.
	if index := strings.LastIndex(text, "image: ${{ vars.CI_JOB_IMAGE }}"); index >= 0 {
		text = text[:index] + strings.Replace(text[index:], "image: ${{ vars.CI_JOB_IMAGE }}", "image: "+config.JavaImage, 1)
	}
	if strings.HasSuffix(file, "/release.yml") {
		text = regexp.MustCompile(`(?m)^(      image: |      version-bump-image: ).*$`).ReplaceAllString(text, "${1}"+config.JavaImage)
		text = strings.ReplaceAll(text, "ARTIFACT_NAME: hello-maven", "ARTIFACT_NAME: "+artifact)
		old := `          cp -- "target/${ARTIFACT_NAME}-${RELEASE_TAG#v}.jar" dist/`
		if !strings.Contains(text, old) {
			return "", fmt.Errorf("native Maven release inventory recipe changed")
		}
		text = strings.Replace(text, old, `          forge-ci publish maven-central validate-artifacts
          cp -- pom.xml "dist/${ARTIFACT_NAME}-${RELEASE_TAG#v}.pom"
          for suffix in .jar -sources.jar -javadoc.jar; do
            cp -- "target/${ARTIFACT_NAME}-${RELEASE_TAG#v}${suffix}" dist/
          done`, 1)
		text = strings.Replace(text, "--asset 'dist/*.jar'", "--asset 'dist/*.jar' \\\n            --asset 'dist/*.pom'", 1)
	}
	return text, nil
}
