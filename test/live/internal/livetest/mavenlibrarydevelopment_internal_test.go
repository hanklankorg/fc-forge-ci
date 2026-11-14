// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestMavenDevelopmentPOMVersion(t *testing.T) {
	for _, version := range []string{"1.2.3", "${revision}", "1.2.3-SNAPSHOT"} {
		_, err := parseMavenDevelopmentPOM("<project><groupId>org.example</groupId><artifactId>library</artifactId><version>" + version + "</version></project>")
		if (err == nil) != strings.HasSuffix(version, "-SNAPSHOT") {
			t.Fatalf("snapshot version contract %s: %v", version, err)
		}
	}
}

const (
	mavenReactorFixture = "test/live/fixtures/development/maven-reactor/consumer"
	mavenReactorOracle  = "test/live/fixtures/oracles/maven-reactor/ReactorSmoke.java"
)

// mavenReactorTestPOMs is a parent aggregator with a library and the
// application that depends on it, as the shipped fixture is shaped.
func mavenReactorTestPOMs() map[string]string {
	const parent = `<parent><groupId>org.example</groupId><artifactId>parent</artifactId><version>1.2.3-SNAPSHOT</version></parent>`

	return map[string]string{
		"pom.xml":         `<project><groupId>org.example</groupId><artifactId>parent</artifactId><version>1.2.3-SNAPSHOT</version><packaging>pom</packaging><modules><module>library</module><module>app</module></modules></project>`,
		"library/pom.xml": `<project>` + parent + `<artifactId>library</artifactId></project>`,
		"app/pom.xml":     `<project>` + parent + `<artifactId>app</artifactId><dependencies><dependency><groupId>org.example</groupId><artifactId>library</artifactId><version>${project.version}</version></dependency></dependencies></project>`,
	}
}

func mavenReactorTestReader(poms map[string]string) func(string) (string, error) {
	return func(path string) (string, error) {
		body, ok := poms[path]
		if !ok {
			return "", os.ErrNotExist
		}

		return body, nil
	}
}

func TestMavenReactorDevelopmentCoordinates(t *testing.T) {
	want := MavenLibraryDevelopment{Group: "org.example", Artifact: "app", Version: "1.2.3-SNAPSHOT", Modules: []MavenDevelopmentModule{
		{Directory: ".", Group: "org.example", Artifact: "parent", Version: "1.2.3-SNAPSHOT", Packaging: "pom"},
		{Directory: "library", Group: "org.example", Artifact: "library", Version: "1.2.3-SNAPSHOT", Packaging: "jar"},
		{Directory: "app", Group: "org.example", Artifact: "app", Version: "1.2.3-SNAPSHOT", Packaging: "jar"},
	}}

	got, err := parseMavenDevelopmentReactor(mavenReactorTestReader(mavenReactorTestPOMs()))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reactor coordinates = %+v, %v", got, err)
	}

	// The consumed module is derived from the sibling edge, not from module
	// order, and a module may state its own group and the shared version.
	reordered := mavenReactorTestPOMs()
	reordered["pom.xml"] = strings.Replace(reordered["pom.xml"], "<module>library</module><module>app</module>", "<module>app</module><module>library</module>", 1)
	reordered["library/pom.xml"] = strings.Replace(reordered["library/pom.xml"], "<artifactId>library</artifactId>", "<groupId>org.example.shared</groupId><artifactId>library</artifactId><version>1.2.3-SNAPSHOT</version>", 1)
	reordered["app/pom.xml"] = strings.Replace(reordered["app/pom.xml"], "<dependency><groupId>org.example</groupId>", "<dependency><groupId>org.example.shared</groupId>", 1)

	got, err = parseMavenDevelopmentReactor(mavenReactorTestReader(reordered))
	if err != nil || got.Artifact != "app" || len(got.Modules) != 3 || got.Modules[1].Directory != "app" || got.Modules[2].Group != "org.example.shared" {
		t.Fatalf("reordered reactor coordinates = %+v, %v", got, err)
	}
}

func TestMavenReactorDevelopmentRefusesWrongShapes(t *testing.T) {
	for name, change := range map[string]func(map[string]string){
		"single library": func(poms map[string]string) {
			poms["pom.xml"] = "<project><groupId>org.example</groupId><artifactId>library</artifactId><version>1.2.3-SNAPSHOT</version></project>"
		},
		"aggregator without modules": func(poms map[string]string) {
			poms["pom.xml"] = "<project><groupId>org.example</groupId><artifactId>parent</artifactId><version>1.2.3-SNAPSHOT</version><packaging>pom</packaging></project>"
		},
		"module missing": func(poms map[string]string) { delete(poms, "app/pom.xml") },
		"module repeated": func(poms map[string]string) {
			poms["pom.xml"] = strings.Replace(poms["pom.xml"], "<module>app</module>", "<module>library</module><module>app</module>", 1)
		},
		"module escapes": func(poms map[string]string) {
			poms["pom.xml"] = strings.Replace(poms["pom.xml"], "<module>app</module>", "<module>../app</module>", 1)
		},
		"module nested": func(poms map[string]string) {
			poms["pom.xml"] = strings.Replace(poms["pom.xml"], "<module>app</module>", "<module>nested/app</module>", 1)
		},
		"version mismatch": func(poms map[string]string) {
			poms["app/pom.xml"] = strings.Replace(poms["app/pom.xml"], "<artifactId>app</artifactId>", "<artifactId>app</artifactId><version>1.2.4-SNAPSHOT</version>", 1)
		},
		"parent version mismatch": func(poms map[string]string) {
			poms["library/pom.xml"] = strings.Replace(poms["library/pom.xml"], "1.2.3-SNAPSHOT", "1.2.2-SNAPSHOT", 1)
		},
		"foreign parent": func(poms map[string]string) {
			poms["library/pom.xml"] = strings.Replace(poms["library/pom.xml"], "<artifactId>parent</artifactId>", "<artifactId>other</artifactId>", 1)
		},
		"war module": func(poms map[string]string) {
			poms["app/pom.xml"] = strings.Replace(poms["app/pom.xml"], "<artifactId>app</artifactId>", "<artifactId>app</artifactId><packaging>war</packaging>", 1)
		},
		"nested aggregator": func(poms map[string]string) {
			poms["library/pom.xml"] = strings.Replace(poms["library/pom.xml"], "<artifactId>library</artifactId>", "<artifactId>library</artifactId><modules><module>inner</module></modules>", 1)
		},
		"property artifact": func(poms map[string]string) {
			poms["app/pom.xml"] = strings.Replace(poms["app/pom.xml"], "<artifactId>app</artifactId>", "<artifactId>${app.name}</artifactId>", 1)
		},
		"no sibling dependency": func(poms map[string]string) {
			poms["app/pom.xml"] = strings.Replace(poms["app/pom.xml"], "<artifactId>library</artifactId><version>", "<artifactId>unrelated</artifactId><version>", 1)
		},
		"one module, depending on no sibling": func(poms map[string]string) {
			poms["pom.xml"] = strings.Replace(poms["pom.xml"], "<module>app</module>", "", 1)
		},
		"sibling only in the test scope": func(poms map[string]string) {
			poms["app/pom.xml"] = strings.Replace(poms["app/pom.xml"], "</dependency>", "<scope>test</scope></dependency>", 1)
		},
		"optional sibling": func(poms map[string]string) {
			poms["app/pom.xml"] = strings.Replace(poms["app/pom.xml"], "</dependency>", "<optional>true</optional></dependency>", 1)
		},
		"sibling at another version": func(poms map[string]string) {
			poms["app/pom.xml"] = strings.Replace(poms["app/pom.xml"], "${project.version}", "1.2.2-SNAPSHOT", 1)
		},
		"sibling cycle": func(poms map[string]string) {
			poms["library/pom.xml"] = strings.Replace(poms["library/pom.xml"], "</project>", "<dependencies><dependency><groupId>org.example</groupId><artifactId>app</artifactId><version>${project.version}</version></dependency></dependencies></project>", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			poms := mavenReactorTestPOMs()
			change(poms)

			if got, err := parseMavenDevelopmentReactor(mavenReactorTestReader(poms)); !errors.Is(err, errDevelopmentReactor) {
				t.Fatalf("admitted or misreported a wrong-shape reactor: %+v, %v", got, err)
			}
		})
	}

	// The aggregator states literal coordinates and a SNAPSHOT version.
	for _, version := range []string{"1.2.3", "${revision}"} {
		poms := mavenReactorTestPOMs()
		poms["pom.xml"] = strings.Replace(poms["pom.xml"], "1.2.3-SNAPSHOT", version, 1)

		if _, err := parseMavenDevelopmentReactor(mavenReactorTestReader(poms)); !errors.Is(err, errDevelopmentPOMIdentity) {
			t.Fatalf("aggregator version %s: %v", version, err)
		}
	}
}

// A wrong-shape reactor is refused at identity, as the unopted aggregator is:
// before the oracle or recipe is read and before the private index changes.
func TestMavenReactorDevelopmentRefusesWithoutChangingSource(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		for name, change := range map[string]func(map[string]string){
			"module missing": func(poms map[string]string) { delete(poms, "app/pom.xml") },
			"version mismatch": func(poms map[string]string) {
				poms["library/pom.xml"] = strings.Replace(poms["library/pom.xml"], "1.2.3-SNAPSHOT", "1.2.2-SNAPSHOT", 1)
			},
			"single library": func(poms map[string]string) {
				poms["pom.xml"] = "<project><groupId>org.example</groupId><artifactId>library</artifactId><version>1.2.3-SNAPSHOT</version></project>"
			},
		} {
			t.Run(format+"/"+name, func(t *testing.T) {
				root, git := stageTestRepo(t, format)
				poms := mavenReactorTestPOMs()
				change(poms)

				for path, body := range poms {
					stageTestWrite(t, root, path, body, 0644)
				}

				stageTestCommit(t, git, root)

				source, err := openConsumer(t.Context(), git, root, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}

				before, err := git.run(t.Context(), source.stage, "write-tree")
				if err != nil {
					t.Fatal(err)
				}

				config := MavenLibraryFixtureConfig{RecipeDirectory: t.TempDir(), JavaImage: "registry.invalid/maven:21@sha256:" + strings.Repeat("a", 64), Profile: "central-release"}

				_, _, err = mavenLibraryDevelopmentFixture(t.Context(), source, config, "/not-read/Oracle.java")
				if !errors.Is(err, errDevelopmentReactor) {
					t.Fatalf("reactor development fixture must refuse before oracle/recipe access: %v", err)
				}

				after, err := git.run(t.Context(), source.stage, "write-tree")
				if err != nil || after != before {
					t.Fatalf("refused reactor changed private index: %q -> %q: %v", before, after, err)
				}

				if err := git.requireClean(t.Context(), root, "."); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestMavenReactorDevelopmentWorkflow(t *testing.T) {
	model, err := parseMavenDevelopmentReactor(mavenReactorTestReader(mavenReactorTestPOMs()))
	if err != nil {
		t.Fatal(err)
	}

	body := mavenDevelopmentWorkflow(MavenLibraryFixtureConfig{JavaImage: "registry.invalid/java@sha256:" + strings.Repeat("a", 64), Profile: "central-release"}, ConsumerPin{Actions: "https://forge.invalid/owner/engine/.forgejo/actions/", Commit: strings.Repeat("a", 40)}, model, "ReactorSmoke")

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		t.Fatal(err)
	}

	runs := map[string]string{}

	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Run == "" {
				continue
			}

			runs[step.Name] = step.Run

			cmd := exec.CommandContext(t.Context(), "bash", "-n")

			cmd.Stdin = strings.NewReader(step.Run)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("shell: %v %s", err, out)
			}
		}
	}

	if len(workflow.Jobs) != 3 || len(runs) != 4 {
		t.Fatalf("expected separate producer, publisher and consumer with four scripts, got %d jobs and %d scripts", len(workflow.Jobs), len(runs))
	}

	// Every module hands over its own POM; only jar modules hand over JARs.
	const export = `set -euo pipefail
mkdir -p dist/
cp pom.xml dist/pom.xml
mkdir -p dist/library/target
cp library/pom.xml dist/library/pom.xml
cp library/target/library-1.2.3-SNAPSHOT.jar library/target/library-1.2.3-SNAPSHOT-sources.jar library/target/library-1.2.3-SNAPSHOT-javadoc.jar dist/library/target/
mkdir -p dist/app/target
cp app/pom.xml dist/app/pom.xml
cp app/target/app-1.2.3-SNAPSHOT.jar app/target/app-1.2.3-SNAPSHOT-sources.jar app/target/app-1.2.3-SNAPSHOT-javadoc.jar dist/app/target/
`
	if got := runs["Export original unsigned library build"]; got != export {
		t.Fatalf("reactor build handoff:\n%s", got)
	}

	// One product invocation publishes the whole handoff.
	if got := runs["Publish prebuilt snapshot to scratch forge registry"]; got != "forge-ci publish forge-packages deploy --project-type maven --from-build --snapshot --working-dir dist --version 1.2.3-SNAPSHOT" {
		t.Fatalf("reactor publication command: %s", got)
	}

	// The fresh consumer builds against the application coordinate alone, then
	// reads back every publication: the aggregator's POM, and each jar module's
	// POM, main, sources and Javadoc JARs, compared with the producer's bytes.
	const consume = `set -euo pipefail
fresh="$(mktemp -d)"
trap 'rm -rf "$fresh"' EXIT
forge-ci artifact download --name "library-$SOURCE_SHA" --dir "$fresh/producer"
cd .forge-ci/registry-consumer
 forge-ci build maven exec --working-dir . -- --batch-mode --no-transfer-progress -U -Dmaven.repo.local="$fresh" package org.apache.maven.plugins:maven-dependency-plugin:3.8.1:copy-dependencies
for type in pom; do
  case "$type" in
    pom) coordinate='org.example:parent:1.2.3-SNAPSHOT:pom' ;;
    jar) coordinate='org.example:parent:1.2.3-SNAPSHOT:jar' ;;
    *) coordinate="org.example:parent:1.2.3-SNAPSHOT:jar:$type" ;;
  esac
   forge-ci build maven exec --working-dir . -- --batch-mode --no-transfer-progress -U -Dmaven.repo.local="$fresh" org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get -Dartifact="$coordinate" -DremoteRepositories="scratch::default::$MAVEN_REGISTRY_URL"
done
group='org.example'
localpath="$fresh/${group//.//}/parent/1.2.3-SNAPSHOT"
cmp "$fresh/producer/pom.xml" "$localpath/parent-1.2.3-SNAPSHOT.pom"
for type in pom jar sources javadoc; do
  case "$type" in
    pom) coordinate='org.example:library:1.2.3-SNAPSHOT:pom' ;;
    jar) coordinate='org.example:library:1.2.3-SNAPSHOT:jar' ;;
    *) coordinate="org.example:library:1.2.3-SNAPSHOT:jar:$type" ;;
  esac
   forge-ci build maven exec --working-dir . -- --batch-mode --no-transfer-progress -U -Dmaven.repo.local="$fresh" org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get -Dartifact="$coordinate" -DremoteRepositories="scratch::default::$MAVEN_REGISTRY_URL"
done
group='org.example'
localpath="$fresh/${group//.//}/library/1.2.3-SNAPSHOT"
cmp "$fresh/producer/library/pom.xml" "$localpath/library-1.2.3-SNAPSHOT.pom"
for suffix in .jar -sources.jar -javadoc.jar; do
  cmp "$fresh/producer/library/target/library-1.2.3-SNAPSHOT$suffix" "$localpath/library-1.2.3-SNAPSHOT$suffix"
done
for type in pom jar sources javadoc; do
  case "$type" in
    pom) coordinate='org.example:app:1.2.3-SNAPSHOT:pom' ;;
    jar) coordinate='org.example:app:1.2.3-SNAPSHOT:jar' ;;
    *) coordinate="org.example:app:1.2.3-SNAPSHOT:jar:$type" ;;
  esac
   forge-ci build maven exec --working-dir . -- --batch-mode --no-transfer-progress -U -Dmaven.repo.local="$fresh" org.apache.maven.plugins:maven-dependency-plugin:3.8.1:get -Dartifact="$coordinate" -DremoteRepositories="scratch::default::$MAVEN_REGISTRY_URL"
done
group='org.example'
localpath="$fresh/${group//.//}/app/1.2.3-SNAPSHOT"
cmp "$fresh/producer/app/pom.xml" "$localpath/app-1.2.3-SNAPSHOT.pom"
for suffix in .jar -sources.jar -javadoc.jar; do
  cmp "$fresh/producer/app/target/app-1.2.3-SNAPSHOT$suffix" "$localpath/app-1.2.3-SNAPSHOT$suffix"
done
java -cp 'target/classes:target/dependency/*' ReactorSmoke
echo "LIBRARY_REGISTRY_ORACLE_OK:$SOURCE_SHA"
`
	if got := runs["Fresh registry consumer and oracle"]; got != consume {
		t.Fatalf("reactor fresh consumer:\n%s", got)
	}

	// A development build has no release authority, runs its tests and
	// builds the library with the project's profile.
	if strings.Contains(body, "RELEASE_") || strings.Contains(body, "skip-tests") || strings.Contains(body, "download-dist") {
		t.Fatal("invalid development authority/build contract")
	}

	for _, want := range []string{"branches: [development]", "--from-build --snapshot", "-Dmaven.repo.local=", "LIBRARY_REGISTRY_ORACLE_OK:", "build-type: lib", "profile: central-release"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s", want)
		}
	}

	if regexp.MustCompile(`@[A-Z]+@`).MatchString(body) {
		t.Fatal("unreplaced workflow placeholder")
	}

	// The rendered handoff is the layout the product's --from-build publisher
	// reads: run it over placeholder build outputs and stage the result. No
	// Maven runs here, so this proves the layout, not a build.
	root := t.TempDir()

	for path, pom := range mavenReactorTestPOMs() {
		stageTestWrite(t, root, path, pom, 0600)
	}

	for _, module := range []string{"library", "app"} {
		for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar"} {
			stageTestWrite(t, root, module+"/target/"+module+"-1.2.3-SNAPSHOT"+suffix, "placeholder build bytes: "+module+suffix, 0600)
		}
	}

	command := exec.CommandContext(t.Context(), "bash", "-c", export)
	command.Dir = root

	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("handoff: %v: %s", err, out)
	}

	destination := t.TempDir()
	if err := apppublish.StageMavenBuild(t.Context(), filepath.Join(root, "dist"), destination, model.Version, true); err != nil {
		t.Fatalf("product refused the rendered reactor handoff: %v", err)
	}

	set, err := apppublish.InspectMavenSnapshot(destination, "")
	if err != nil {
		t.Fatal(err)
	}

	payloads := make([]string, 0, 9)
	payloads = append(payloads, "org/example/parent/1.2.3-SNAPSHOT/parent-1.2.3-SNAPSHOT.pom")

	for _, module := range []string{"library", "app"} {
		for _, suffix := range []string{".pom", ".jar", "-sources.jar", "-javadoc.jar"} {
			payloads = append(payloads, "org/example/"+module+"/1.2.3-SNAPSHOT/"+module+"-1.2.3-SNAPSHOT"+suffix)
		}
	}

	slices.Sort(payloads)

	if len(set.Publications) != len(model.Modules) || !slices.Equal(set.Payloads, payloads) {
		t.Fatalf("product publication inventory of the rendered handoff: %+v", set)
	}
}

// The shipped reactor fixture is admitted, keeps its
// source bytes, and gives the fresh consumer the application coordinate.
func TestMavenReactorDevelopmentFixture(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root, git := stageTestRepo(t, format)
			if err := os.CopyFS(root, os.DirFS(filepath.Join(reporoot.Path(t), mavenReactorFixture))); err != nil {
				t.Fatal(err)
			}

			stageTestCommit(t, git, root)

			source, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			config := MavenLibraryFixtureConfig{RecipeDirectory: filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-maven/consumer"), JavaImage: "registry.invalid/maven:21@sha256:" + strings.Repeat("a", 64), Profile: "central-release"}
			oracle := filepath.Join(reporoot.Path(t), mavenReactorOracle)

			fixture, model, err := mavenLibraryDevelopmentFixture(t.Context(), source, config, oracle)
			if err != nil {
				t.Fatal(err)
			}

			const group, version = "org.example.fixture.reactor", "0.1.0-SNAPSHOT"

			want := MavenLibraryDevelopment{Group: group, Artifact: "app", Version: version, Modules: []MavenDevelopmentModule{
				{Directory: ".", Group: group, Artifact: "parent", Version: version, Packaging: "pom"},
				{Directory: "library", Group: group, Artifact: "library", Version: version, Packaging: "jar"},
				{Directory: "app", Group: group, Artifact: "app", Version: version, Packaging: "jar"},
			}}
			if !reflect.DeepEqual(model, want) {
				t.Fatalf("fixture identity = %+v", model)
			}

			for _, pom := range []string{"pom.xml", "library/pom.xml", "app/pom.xml"} {
				original, err := os.ReadFile(filepath.Join(root, pom))
				if err != nil {
					t.Fatal(err)
				}

				if got, err := git.output(t.Context(), fixture.stage, nil, "show", fixture.Commit+":"+pom); err != nil || string(got) != string(original) {
					t.Fatalf("source %s changed: %v", pom, err)
				}
			}

			consumer, err := git.run(t.Context(), fixture.stage, "show", fixture.Commit+":.forge-ci/registry-consumer/pom.xml")
			if err != nil || !strings.Contains(consumer, "<dependencies><dependency><groupId>"+group+"</groupId><artifactId>app</artifactId><version>"+version+"</version></dependency></dependencies>") || strings.Contains(consumer, "<artifactId>library</artifactId>") || strings.Contains(consumer, "<artifactId>parent</artifactId>") {
				t.Fatalf("fresh consumer must declare the application alone: %s %v", consumer, err)
			}

			smoke, err := os.ReadFile(oracle)
			if err != nil {
				t.Fatal(err)
			}

			if got, err := git.output(t.Context(), fixture.stage, nil, "show", fixture.Commit+":.forge-ci/registry-consumer/src/main/java/ReactorSmoke.java"); err != nil || string(got) != string(smoke) {
				t.Fatalf("oracle not staged unchanged: %v", err)
			}

			workflow, err := git.run(t.Context(), fixture.stage, "show", fixture.Commit+":"+forgejoDevelopmentWorkflow)
			if err != nil || !strings.Contains(workflow, "cp app/target/app-"+version+".jar") || !strings.Contains(workflow, "java -cp 'target/classes:target/dependency/*' ReactorSmoke") {
				t.Fatalf("development workflow not staged for the reactor: %v", err)
			}

			if err := git.requireClean(t.Context(), root, "."); err != nil {
				t.Fatal("original source checkout modified")
			}

			// The product's own publisher admits the fixture's POMs as they are.
			handoff := t.TempDir()
			if err := os.CopyFS(handoff, os.DirFS(root)); err != nil {
				t.Fatal(err)
			}

			for _, module := range []string{"library", "app"} {
				for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar"} {
					stageTestWrite(t, handoff, module+"/target/"+module+"-"+version+suffix, "placeholder build bytes", 0600)
				}
			}

			if err := apppublish.StageMavenBuild(t.Context(), handoff, t.TempDir(), version, true); err != nil {
				t.Fatalf("product refused the fixture's reactor: %v", err)
			}
		})
	}
}
