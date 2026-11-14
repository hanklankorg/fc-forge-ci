// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The consumer paths of the Forgejo release workflow and of the overlaid
// development workflow.
const (
	forgejoReleaseWorkflow     = ".forgejo/workflows/release.yml"
	forgejoDevelopmentWorkflow = ".forgejo/workflows/development.yml"
)

var (
	errOracleJavaPath         = errors.New("oracle must be an absolute Java source path with a simple class name")
	errOracleNotRegular       = errors.New("oracle must be a regular source file")
	errDevelopmentPOMIdentity = errors.New("development requires literal group/artifact and an existing SNAPSHOT version")
	errDevelopmentReactor     = errors.New("development reactor requires a pom aggregator of literal jar modules sharing its version, one of which depends on a sibling and has no dependent sibling")

	// mavenLiteral is a coordinate or module directory stated without a property.
	mavenLiteral = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// The packagings the Maven library fixtures admit.
const (
	mavenPackagingJar = "jar"
	mavenPackagingPOM = "pom"
)

// MavenLibraryDevelopment is the admitted reactor's native snapshot identity:
// the coordinate the fresh registry consumer declares, the one module no
// sibling depends on, and every publication of the development workflow,
// the aggregator first.
type MavenLibraryDevelopment struct {
	Group, Artifact, Version string
	Modules                  []MavenDevelopmentModule
}

// MavenDevelopmentModule is one publication of the development workflow.
// Directory is "." for the root; Packaging is jar or, for the aggregator, pom.
type MavenDevelopmentModule struct{ Directory, Group, Artifact, Version, Packaging string }

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

	// The admitted revision's regular POM blobs, read by path.
	identity, err := parseMavenDevelopmentReactor(func(path string) (string, error) { return githubWorkflow(ctx, source, path) })
	if err != nil {
		return source, identity, err
	}

	if !filepath.IsAbs(oracle) || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*\.java$`).MatchString(filepath.Base(oracle)) {
		return source, identity, errOracleJavaPath
	}

	info, err := os.Lstat(oracle)
	if err != nil {
		return source, identity, err
	}

	if !info.Mode().IsRegular() {
		return source, identity, errOracleNotRegular
	}

	smoke, err := os.ReadFile(oracle) //nolint:gosec // operator-supplied oracle, checked above as an absolute regular Java source file.
	if err != nil {
		return source, identity, err
	}

	recipe, err := os.ReadFile(filepath.Join(config.RecipeDirectory, ".forgejo/workflows/release.yml"))
	if err != nil {
		return source, identity, err
	}

	pin, err := ReadConsumerPin(map[string]string{trialReleaseWorkflow: string(recipe)})
	if err != nil {
		return source, identity, err
	}

	workflow := mavenDevelopmentWorkflow(config, pin, identity, strings.TrimSuffix(filepath.Base(oracle), ".java"))

	smokePOM := `<project><modelVersion>4.0.0</modelVersion><groupId>org.example.fixture</groupId><artifactId>registry-consumer</artifactId><version>1</version><properties><maven.compiler.release>21</maven.compiler.release></properties><dependencies><dependency><groupId>` + identity.Group + `</groupId><artifactId>` + identity.Artifact + `</artifactId><version>` + identity.Version + `</version></dependency></dependencies><repositories><repository><id>scratch</id><url>${env.MAVEN_REGISTRY_URL}</url><snapshots><enabled>true</enabled><updatePolicy>always</updatePolicy></snapshots></repository></repositories><build><plugins><plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-compiler-plugin</artifactId><version>3.15.0</version></plugin></plugins></build></project>`
	if _, err = source.git.run(ctx, source.stage, "read-tree", source.Commit); err != nil {
		return source, identity, err
	}

	for name, body := range map[string]string{forgejoDevelopmentWorkflow: workflow, ".forge-ci/registry-consumer/pom.xml": smokePOM, ".forge-ci/registry-consumer/src/main/java/" + filepath.Base(oracle): string(smoke)} {
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
		return MavenLibraryDevelopment{}, errDevelopmentPOMIdentity
	}

	return MavenLibraryDevelopment{Group: model.Group, Artifact: model.Artifact, Version: model.Version}, nil
}

// mavenReactorPOM is what the development reactor reads of each POM.
type mavenReactorPOM struct {
	Group     string   `xml:"groupId"`
	Artifact  string   `xml:"artifactId"`
	Version   string   `xml:"version"`
	Packaging string   `xml:"packaging"`
	Modules   []string `xml:"modules>module"`
	Parent    struct {
		Group    string `xml:"groupId"`
		Artifact string `xml:"artifactId"`
		Version  string `xml:"version"`
	} `xml:"parent"`
	Dependencies []struct {
		Group    string `xml:"groupId"`
		Artifact string `xml:"artifactId"`
		Version  string `xml:"version"`
		Scope    string `xml:"scope"`
		Type     string `xml:"type"`
		Optional string `xml:"optional"`
		Class    string `xml:"classifier"`
	} `xml:"dependencies>dependency"`
}

// parseMavenDevelopmentReactor derives every publication from the POMs read
// by path: a SNAPSHOT pom aggregator and its direct jar modules, each naming
// the aggregator as parent at its version. The product publishes each
// module's own POM bytes, so a fresh consumer resolves the parent and the
// siblings from the registry. The consumed module is the one no sibling
// depends on; it must itself depend on a sibling at the shared version.
func parseMavenDevelopmentReactor(read func(path string) (string, error)) (MavenLibraryDevelopment, error) {
	body, err := read("pom.xml")
	if err != nil {
		return MavenLibraryDevelopment{}, err
	}

	root, err := parseMavenDevelopmentPOM(body)
	if err != nil {
		return MavenLibraryDevelopment{}, err
	}

	var aggregator mavenReactorPOM
	if err = xml.Unmarshal([]byte(body), &aggregator); err != nil {
		return MavenLibraryDevelopment{}, err
	}

	if aggregator.Packaging != mavenPackagingPOM || len(aggregator.Modules) == 0 {
		return MavenLibraryDevelopment{}, fmt.Errorf("%w: root is not an aggregator", errDevelopmentReactor)
	}

	modules := []MavenDevelopmentModule{{Directory: ".", Group: root.Group, Artifact: root.Artifact, Version: root.Version, Packaging: mavenPackagingPOM}}
	children := make([]mavenReactorPOM, 0, len(aggregator.Modules))

	for _, directory := range aggregator.Modules {
		if !mavenLiteral.MatchString(directory) || slices.ContainsFunc(modules, func(module MavenDevelopmentModule) bool { return module.Directory == directory }) {
			return MavenLibraryDevelopment{}, fmt.Errorf("%w: module %q is not a distinct direct subdirectory", errDevelopmentReactor, directory)
		}

		body, err = read(directory + "/pom.xml")
		if err != nil {
			return MavenLibraryDevelopment{}, fmt.Errorf("%w: module %s: %w", errDevelopmentReactor, directory, err)
		}

		child, err := parseMavenReactorModule(body, root)
		if err != nil {
			return MavenLibraryDevelopment{}, fmt.Errorf("%w: module %s", err, directory)
		}

		modules = append(modules, MavenDevelopmentModule{Directory: directory, Group: child.Group, Artifact: child.Artifact, Version: child.Version, Packaging: mavenPackagingJar})
		children = append(children, child)
	}

	consumed, err := mavenReactorConsumed(children, root.Version)
	if err != nil {
		return MavenLibraryDevelopment{}, err
	}

	return MavenLibraryDevelopment{Group: consumed.Group, Artifact: consumed.Artifact, Version: consumed.Version, Modules: modules}, nil
}

// parseMavenReactorModule resolves a module's inherited group and version and
// requires a literal jar module that is the aggregator's child at its version.
func parseMavenReactorModule(body string, root MavenLibraryDevelopment) (mavenReactorPOM, error) {
	var child mavenReactorPOM
	if err := xml.Unmarshal([]byte(body), &child); err != nil {
		return child, err
	}

	if child.Group == "" {
		child.Group = child.Parent.Group
	}

	if child.Version == "" {
		child.Version = child.Parent.Version
	}

	if child.Parent.Group != root.Group || child.Parent.Artifact != root.Artifact || child.Parent.Version != root.Version || child.Version != root.Version {
		return child, fmt.Errorf("%w: not the aggregator's child at its version", errDevelopmentReactor)
	}

	if !mavenLiteral.MatchString(child.Group) || !mavenLiteral.MatchString(child.Artifact) || (child.Packaging != "" && child.Packaging != mavenPackagingJar) || len(child.Modules) != 0 {
		return child, fmt.Errorf("%w: not a literal jar module", errDevelopmentReactor)
	}

	return child, nil
}

// mavenReactorConsumed follows the sibling edges: a module with a dependent
// is not the consumed one, and the consumed one must reach a sibling at the
// shared version so the fresh consumer's resolution is transitive. Only an
// edge a consumer inherits counts: the sibling's plain JAR, required, in the
// compile or runtime scope.
func mavenReactorConsumed(children []mavenReactorPOM, version string) (mavenReactorPOM, error) {
	depended, depends := map[string]bool{}, map[string]bool{}

	for _, child := range children {
		for _, dependency := range child.Dependencies {
			group := strings.ReplaceAll(dependency.Group, "${project.groupId}", child.Group)
			sibling := slices.ContainsFunc(children, func(other mavenReactorPOM) bool { return other.Group == group && other.Artifact == dependency.Artifact })

			if sibling && dependency.Version != "${project.version}" && dependency.Version != version {
				return mavenReactorPOM{}, fmt.Errorf("%w: %s depends on sibling %s at another version", errDevelopmentReactor, child.Artifact, dependency.Artifact)
			}

			if sibling && mavenInheritedEdge(dependency.Scope, dependency.Type, dependency.Optional, dependency.Class) {
				depended[group+":"+dependency.Artifact], depends[child.Group+":"+child.Artifact] = true, true
			}
		}
	}

	consumed := slices.DeleteFunc(slices.Clone(children), func(child mavenReactorPOM) bool { return depended[child.Group+":"+child.Artifact] })
	if len(consumed) != 1 || !depends[consumed[0].Group+":"+consumed[0].Artifact] {
		return mavenReactorPOM{}, fmt.Errorf("%w: want one module no sibling depends on, itself depending on a sibling; %d have no dependent", errDevelopmentReactor, len(consumed))
	}

	return consumed[0], nil
}

// mavenInheritedEdge reports whether a dependency reaches the consumers of the
// module that declares it: a plain JAR, required, in the compile or runtime
// scope.
func mavenInheritedEdge(scope, kind, optional, classifier string) bool {
	skipped, _ := strconv.ParseBool(strings.TrimSpace(optional))

	return (scope == "" || scope == "compile" || scope == "runtime") && (kind == "" || kind == mavenPackagingJar) && !skipped && classifier == ""
}

// prefix is the module's path prefix in the checkout and in the build handoff.
func (module MavenDevelopmentModule) prefix() string {
	if module.Directory == "." {
		return ""
	}

	return module.Directory + "/"
}

// mavenDevelopmentExport copies each publication's original POM, and a jar
// module's main, sources and Javadoc JARs, to the layout the product's
// --from-build publisher reads: <module>/pom.xml and <module>/target/.
func mavenDevelopmentExport(modules []MavenDevelopmentModule) string {
	lines := make([]string, 0, 3*len(modules))

	for _, module := range modules {
		prefix := module.prefix()
		if module.Packaging == mavenPackagingPOM {
			lines = append(lines, "mkdir -p dist/"+prefix, "cp "+prefix+"pom.xml dist/"+prefix+"pom.xml")

			continue
		}

		stem := prefix + "target/" + module.Artifact + "-" + module.Version
		lines = append(lines, "mkdir -p dist/"+prefix+"target", "cp "+prefix+"pom.xml dist/"+prefix+"pom.xml", "cp "+stem+".jar "+stem+"-sources.jar "+stem+"-javadoc.jar dist/"+prefix+"target/")
	}

	return strings.Join(lines, "\n          ")
}

// mavenDevelopmentReadback resolves each publication's files by coordinate
// into the fresh local repository and compares them with the producer's
// bytes. An aggregator has a POM only.
func mavenDevelopmentReadback(modules []MavenDevelopmentModule) string {
	blocks := make([]string, 0, len(modules))

	for _, module := range modules {
		block, types := mavenDevelopmentReadbackPOM+mavenDevelopmentReadbackJars, "pom jar sources javadoc"
		if module.Packaging == mavenPackagingPOM {
			block, types = mavenDevelopmentReadbackPOM, mavenPackagingPOM
		}

		blocks = append(blocks, strings.NewReplacer("@TYPES@", types, "@PREFIX@", module.prefix(), "@GROUP@", module.Group, "@ARTIFACT@", module.Artifact, "@VERSION@", module.Version).Replace(block))
	}

	return strings.Join(blocks, "\n          ")
}

const (
	mavenDevelopmentReadbackPOM = `for type in @TYPES@; do
            case "$type" in
              pom) coordinate='@GROUP@:@ARTIFACT@:@VERSION@:pom' ;;
              jar) coordinate='@GROUP@:@ARTIFACT@:@VERSION@:jar' ;;
              *) coordinate="@GROUP@:@ARTIFACT@:@VERSION@:jar:$type" ;;
            esac
             forge-ci build maven exec --working-dir . -- --batch-mode --no-transfer-progress -U -Dmaven.repo.local="$fresh" org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get -Dartifact="$coordinate" -DremoteRepositories="scratch::default::$MAVEN_REGISTRY_URL"
          done
          group='@GROUP@'
          localpath="$fresh/${group//.//}/@ARTIFACT@/@VERSION@"
          cmp "$fresh/producer/@PREFIX@pom.xml" "$localpath/@ARTIFACT@-@VERSION@.pom"`
	mavenDevelopmentReadbackJars = `
          for suffix in .jar -sources.jar -javadoc.jar; do
            cmp "$fresh/producer/@PREFIX@target/@ARTIFACT@-@VERSION@$suffix" "$localpath/@ARTIFACT@-@VERSION@$suffix"
          done`
)

func mavenDevelopmentWorkflow(config MavenLibraryFixtureConfig, pin ConsumerPin, model MavenLibraryDevelopment, main string) string {
	return strings.NewReplacer("@IMAGE@", config.JavaImage, "@PROFILE@", config.Profile, "@ACTIONS@", pin.Actions, "@PIN@", pin.Commit, "@EXPORT@", mavenDevelopmentExport(model.Modules), "@READBACK@", mavenDevelopmentReadback(model.Modules), "@VERSION@", model.Version, "@MAIN@", main).Replace(`name: Library development
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
          @EXPORT@
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
           forge-ci build maven exec --working-dir . -- --batch-mode --no-transfer-progress -U -Dmaven.repo.local="$fresh" package org.apache.maven.plugins:maven-dependency-plugin:3.8.1:copy-dependencies
          @READBACK@
          java -cp 'target/classes:target/dependency/*' @MAIN@
          echo "LIBRARY_REGISTRY_ORACLE_OK:$SOURCE_SHA"
`)
}
