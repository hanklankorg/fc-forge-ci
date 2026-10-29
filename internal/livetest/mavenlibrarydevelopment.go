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

// MavenLibraryDevelopment is the admitted project's native snapshot identity.
type MavenLibraryDevelopment struct{ Group, Artifact, Version string }

// NewMavenLibraryDevelopmentFixture overlays only a private index. Oracle is a
// caller-owned Java source file, kept separate from generic library behavior.
func NewMavenLibraryDevelopmentFixture(tb TB, source Consumer, config MavenLibraryFixtureConfig, oracle string) (Consumer, MavenLibraryDevelopment) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()
	fixture, model, err := mavenLibraryDevelopmentFixture(ctx, source, config, oracle)
	if err != nil {
		tb.Fatalf("Maven development fixture: %v", err)
	}
	return fixture, model
}

func mavenLibraryDevelopmentFixture(ctx context.Context, source Consumer, config MavenLibraryFixtureConfig, oracle string) (Consumer, MavenLibraryDevelopment, error) {
	var identity MavenLibraryDevelopment
	if err := config.validate(); err != nil {
		return source, identity, err
	}
	if _, err := mavenLibraryArtifact(ctx, source); err != nil {
		return source, identity, err
	}
	pom, err := githubWorkflow(ctx, source, "pom.xml")
	if err != nil {
		return source, identity, err
	}
	identity, err = parseMavenDevelopmentPOM(pom)
	if err != nil {
		return source, identity, err
	}
	if !filepath.IsAbs(oracle) || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*\.java$`).MatchString(filepath.Base(oracle)) {
		return source, identity, fmt.Errorf("oracle must be an absolute Java source path with a simple class name")
	}
	info, err := os.Lstat(oracle)
	if err != nil {
		return source, identity, err
	}
	if !info.Mode().IsRegular() {
		return source, identity, fmt.Errorf("oracle must be a regular source file")
	}
	smoke, err := os.ReadFile(oracle)
	if err != nil {
		return source, identity, err
	}
	recipe, err := os.ReadFile(filepath.Join(config.RecipeDirectory, ".forgejo/workflows/release.yml"))
	if err != nil {
		return source, identity, err
	}
	pin, err := ReadConsumerPin(map[string]string{"release.yml": string(recipe)})
	if err != nil {
		return source, identity, err
	}
	workflow := mavenDevelopmentWorkflow(config, pin, identity, strings.TrimSuffix(filepath.Base(oracle), ".java"))
	smokePOM := `<project><modelVersion>4.0.0</modelVersion><groupId>org.example.fixture</groupId><artifactId>registry-consumer</artifactId><version>1</version><properties><maven.compiler.release>21</maven.compiler.release></properties><dependencies><dependency><groupId>` + identity.Group + `</groupId><artifactId>` + identity.Artifact + `</artifactId><version>` + identity.Version + `</version></dependency></dependencies><repositories><repository><id>scratch</id><url>${env.MAVEN_REGISTRY_URL}</url><snapshots><enabled>true</enabled><updatePolicy>always</updatePolicy></snapshots></repository></repositories><build><plugins><plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-compiler-plugin</artifactId><version>3.15.0</version></plugin></plugins></build></project>`
	if _, err = source.git.run(ctx, source.stage, "read-tree", source.Commit); err != nil {
		return source, identity, err
	}
	for name, body := range map[string]string{".forgejo/workflows/development.yml": workflow, ".forge-ci/registry-consumer/pom.xml": smokePOM, ".forge-ci/registry-consumer/src/main/java/" + filepath.Base(oracle): string(smoke)} {
		if err = source.stageFile(ctx, "100644", name, body); err != nil {
			return source, identity, err
		}
	}
	source.Commit, err = source.commit(ctx, consumerBaseMessage)
	return source, identity, err
}

func parseMavenDevelopmentPOM(pom string) (MavenLibraryDevelopment, error) {
	var model struct {
		Group    string `xml:"groupId"`
		Artifact string `xml:"artifactId"`
		Version  string `xml:"version"`
	}
	if err := xml.Unmarshal([]byte(pom), &model); err != nil {
		return MavenLibraryDevelopment{}, err
	}
	safe := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	if !safe.MatchString(model.Group) || !safe.MatchString(model.Artifact) || !safe.MatchString(model.Version) || !strings.HasSuffix(model.Version, "-SNAPSHOT") {
		return MavenLibraryDevelopment{}, fmt.Errorf("development requires literal group/artifact and an existing SNAPSHOT version")
	}
	return MavenLibraryDevelopment{model.Group, model.Artifact, model.Version}, nil
}

func mavenDevelopmentWorkflow(config MavenLibraryFixtureConfig, pin ConsumerPin, model MavenLibraryDevelopment, main string) string {
	return strings.NewReplacer("@IMAGE@", config.JavaImage, "@PROFILE@", config.Profile, "@ACTIONS@", pin.Actions, "@PIN@", pin.Commit, "@GROUP@", model.Group, "@ARTIFACT@", model.Artifact, "@VERSION@", model.Version, "@MAIN@", main).Replace(`name: Library development
on:
  push:
    branches: [development]
jobs:
  build:
    runs-on: ${{ fromJSON(vars.CI_RUNNER_LABEL) }}
    container:
      image: @IMAGE@
    steps:
      - uses: @ACTIONS@setup-forge-ci@@PIN@
      - uses: @ACTIONS@checkout-consumer@@PIN@
      - uses: @ACTIONS@build-maven@@PIN@
        with:
          commit: ${{ forgejo.sha }}
          build-type: lib
          profile: @PROFILE@
      - name: Export original unsigned library build
        run: |
          set -euo pipefail
          mkdir -p dist/target
          cp pom.xml dist/pom.xml
          cp target/@ARTIFACT@-@VERSION@.jar target/@ARTIFACT@-@VERSION@-sources.jar target/@ARTIFACT@-@VERSION@-javadoc.jar dist/target/
      - uses: @ACTIONS@upload-dist@@PIN@
        with:
          artifact-name: library-${{ forgejo.sha }}
          path: dist/
  publish:
    needs: build
    runs-on: ${{ fromJSON(vars.CI_RUNNER_LABEL) }}
    container:
      image: @IMAGE@
    steps:
      - uses: @ACTIONS@setup-forge-ci@@PIN@
      - uses: @ACTIONS@checkout-consumer@@PIN@
      - name: Download current-source build
        env:
          SOURCE_SHA: ${{ forgejo.sha }}
        run: forge-ci artifact download --name "library-$SOURCE_SHA" --dir dist
      - name: Publish prebuilt snapshot to scratch forge registry
        env:
          FORGEJO_TOKEN: ${{ secrets.DEVELOPMENT_REGISTRY_TOKEN }}
        run: forge-ci publish forge-packages deploy --project-type maven --from-build --snapshot --working-dir dist --version @VERSION@
  consume:
    needs: publish
    runs-on: ${{ fromJSON(vars.CI_RUNNER_LABEL) }}
    container:
      image: @IMAGE@
    steps:
      - uses: @ACTIONS@setup-forge-ci@@PIN@
      - uses: @ACTIONS@checkout-consumer@@PIN@
      - name: Fresh registry consumer and oracle
        env:
          MAVEN_REGISTRY_URL: ${{ forgejo.server_url }}/api/packages/${{ forgejo.repository_owner }}/maven
          SOURCE_SHA: ${{ forgejo.sha }}
        run: |
          set -euo pipefail
          fresh="$(mktemp -d)"
          trap 'rm -rf "$fresh"' EXIT
          forge-ci artifact download --name "library-$SOURCE_SHA" --dir "$fresh/producer"
          cd .forge-ci/registry-consumer
          mvn --batch-mode --no-transfer-progress -U -Dmaven.repo.local="$fresh" package org.apache.maven.plugins:maven-dependency-plugin:3.8.1:copy-dependencies
          for type in pom jar sources javadoc; do
            case "$type" in
              pom) coordinate='@GROUP@:@ARTIFACT@:@VERSION@:pom' ;;
              jar) coordinate='@GROUP@:@ARTIFACT@:@VERSION@:jar' ;;
              *) coordinate="@GROUP@:@ARTIFACT@:@VERSION@:jar:$type" ;;
            esac
            mvn --batch-mode --no-transfer-progress -U -Dmaven.repo.local="$fresh" org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get -Dartifact="$coordinate" -DremoteRepositories="scratch::default::$MAVEN_REGISTRY_URL"
          done
          group='@GROUP@'
          localpath="$fresh/${group//.//}/@ARTIFACT@/@VERSION@"
          cmp "$fresh/producer/pom.xml" "$localpath/@ARTIFACT@-@VERSION@.pom"
          for suffix in .jar -sources.jar -javadoc.jar; do
            cmp "$fresh/producer/target/@ARTIFACT@-@VERSION@$suffix" "$localpath/@ARTIFACT@-@VERSION@$suffix"
          done
          java -cp 'target/classes:target/dependency/*' @MAIN@
          echo "LIBRARY_REGISTRY_ORACLE_OK:$SOURCE_SHA"
`)
}
