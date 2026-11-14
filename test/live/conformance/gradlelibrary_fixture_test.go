// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// The Gradle JVM library witness: a real Gradle producer, the product's own
// export and registry upload, and a second Gradle build that knows the library
// only as a coordinate in the forge's registry. This file carries no build tag,
// so the fixture, the rendered job and both oracles are checked in every test
// run; the live owner is gradlelibrary_test.go.
const (
	gradleLibraryGroup    = "org.example.fixture"
	gradleLibraryArtifact = "hello-gradle-library"
	// gradleLibraryGreeting is what the consumer prints, and only the
	// producer's Greeting class returns it.
	gradleLibraryGreeting = "hello, fresh consumer, from hello-gradle-library"
	gradleLibraryReceipt  = "gradle-publication.sha256"

	gradleLibraryFixture = "../fixtures/libraries/gradle-jvm"
	// The wrapper is the one the Gradle demo vendors, with its pinned
	// distribution checksum, so the repository holds one copy of the JAR.
	gradleWrapperSource = "../fixtures/consumers/hello-gradle/consumer"
)

// gradleLibraryFiles is the one commit the job downloads: the producer and the
// consumer as two self-contained builds, each with the wrapper, and the
// producer's version for this run.
func gradleLibraryFiles(version string) (map[string]string, error) {
	files := map[string]string{"producer/gradle.properties": "version=" + version + "\n"}

	for _, build := range []string{"producer", "consumer"} {
		tree := os.DirFS(filepath.Join(gradleLibraryFixture, build))

		err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}

			body, err := fs.ReadFile(tree, name)
			files[build+"/"+name] = string(body)

			return err
		})
		if err != nil {
			return nil, err
		}

		for _, name := range []string{"gradlew", "gradle/wrapper/gradle-wrapper.jar", "gradle/wrapper/gradle-wrapper.properties"} {
			body, err := os.ReadFile(filepath.Join(gradleWrapperSource, name))
			if err != nil {
				return nil, err
			}

			files[build+"/"+name] = string(body)
		}
	}

	return files, nil
}

// gradleLibraryProbe is the one job: export with the product, freeze the
// exported bytes, upload with the product, then resolve and run the library
// from a second build whose Gradle home holds the distribution and nothing
// else. Forgejo only: the fixture reaches the job as a commit of the scratch
// repository, which the kit writes through Forgejo's contents API.
func gradleLibraryProbe(target livetest.Target, prelude, repo, commit, version, tag string) string {
	coordinate := strings.ReplaceAll(gradleLibraryGroup, ".", "/") + "/" + gradleLibraryArtifact

	// The JVM keeps its own trust store, as the Maven probes found.
	commands := prelude + `
keytool -importcert -noprompt -alias forge-lab -cacerts -storepass changeit -file ` + livetest.LabCAPath + `
version='` + version + `'
stem="` + gradleLibraryArtifact + `-$version"
exported="publication/` + coordinate + `/$version"
curl -fsSL -o fixture.tar.gz '` + target.BaseURL() + "/" + target.Owner + "/" + repo + "/archive/" + commit + `.tar.gz'
mkdir fixture
tar -xzf fixture.tar.gz -C fixture --strip-components=1
# The contents API stores no execute bit, and the export runs no build before it.
chmod +x fixture/producer/gradlew fixture/consumer/gradlew
export GRADLE_USER_HOME="$PWD/producer-gradle-home"
run_product build gradle export-publications --working-dir fixture/producer --output publication --version "$version"
# Frozen before the upload, so the registry is held to what Gradle produced.
( cd "$exported" && sha256sum "$stem.pom" "$stem.module" "$stem.jar" "$stem-sources.jar" ) > ` + gradleLibraryReceipt + `
run_product release attachments upload --tag ` + tag + ` --pattern ` + gradleLibraryReceipt + `
run_product publish forge-packages deploy --project-type gradle --working-dir publication --version "$version"
# A fresh consumer: another build, no token, and a Gradle home that holds the
# distribution alone, so the dependency cache starts empty.
consumer_home="$PWD/consumer-gradle-home"
test ! -e "$consumer_home"
mkdir -p "$consumer_home/wrapper"
cp -R "$GRADLE_USER_HOME/wrapper/dists" "$consumer_home/wrapper/dists"
( cd fixture/consumer && env -u FORGEJO_TOKEN GRADLE_USER_HOME="$consumer_home" ./gradlew --no-daemon --quiet run \
    -PregistryUrl='` + target.BaseURL() + "/api/packages/" + target.Owner + `/maven' -PlibraryVersion="$version" ) > consumer-out.txt
cat consumer-out.txt
grep -Fxq '` + gradleLibraryGreeting + `' consumer-out.txt
# What Gradle resolved is the producer's JAR, found through the producer's
# module metadata: both are in the consumer's cache, byte for byte.
resolved="$consumer_home/caches/modules-2/files-2.1/` + gradleLibraryGroup + "/" + gradleLibraryArtifact + `/$version"
for file in "$stem.jar" "$stem.module"; do
  test "$(find "$resolved" -type f -name "$file" | wc -l)" -eq 1
  cmp "$exported/$file" "$(find "$resolved" -type f -name "$file")"
done
`

	// The JDK image the Maven probes run in; Maven itself is not used.
	return `on: [push]
jobs:
  publish:
    runs-on: ubuntu-latest
    container:
      image: maven:3.9-eclipse-temurin-21
    steps:
      - name: publish a Gradle library and consume it from the registry
        env:
          FORGEJO_TOKEN: ${{ secrets.RC_PACKAGE_TOKEN }}
        run: |
          ` + indent(commands, 10) + "\n"
}

// checksumReceipt reads `sha256sum` output that must name exactly the given
// files, once each, and returns each file's digest.
func checksumReceipt(receipt []byte, names ...string) (map[string]string, error) {
	digests := make(map[string]string, len(names))

	for line := range strings.SplitSeq(strings.TrimSpace(string(receipt)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !container.ValidSHA256Hex(fields[0]) {
			return nil, fmt.Errorf("receipt line %q is not a SHA-256 and a name: %w", line, errs.ErrValidation)
		}

		if _, twice := digests[fields[1]]; twice || !slices.Contains(names, fields[1]) {
			return nil, fmt.Errorf("receipt names %q, unexpectedly or twice: %w", fields[1], errs.ErrValidation)
		}

		digests[fields[1]] = fields[0]
	}

	if len(digests) != len(names) {
		return nil, fmt.Errorf("receipt names %d of %d files: %w", len(digests), len(names), errs.ErrValidation)
	}

	return digests, nil
}

// gradlePublicationFindings holds the registry's Gradle module metadata and POM
// to the fixture's coordinate at one version, and to the JAR the registry
// serves beside them. Empty means a Gradle consumer is told, by a Gradle
// producer, to fetch exactly those bytes.
func gradlePublicationFindings(module, pom, jar []byte, version string) []string {
	var findings []string

	var metadata struct {
		Component struct {
			Group   string `json:"group"`
			Module  string `json:"module"`
			Version string `json:"version"`
		} `json:"component"`
		CreatedBy struct {
			Gradle struct {
				Version string `json:"version"`
			} `json:"gradle"`
		} `json:"createdBy"`
		Variants []struct {
			Name  string `json:"name"`
			Files []struct {
				Name   string `json:"name"`
				Size   int    `json:"size"`
				SHA256 string `json:"sha256"`
			} `json:"files"`
		} `json:"variants"`
	}

	if err := json.Unmarshal(module, &metadata); err != nil {
		return []string{"the module metadata is not JSON: " + err.Error()}
	}

	if got := metadata.Component; got.Group != gradleLibraryGroup || got.Module != gradleLibraryArtifact || got.Version != version {
		findings = append(findings, fmt.Sprintf("the module metadata describes %+v, want %s:%s:%s", got, gradleLibraryGroup, gradleLibraryArtifact, version))
	}

	if metadata.CreatedBy.Gradle.Version == "" {
		findings = append(findings, "the module metadata names no Gradle version that created it")
	}

	digest := sha256.Sum256(jar)
	name := gradleLibraryArtifact + "-" + version + ".jar"

	// What a consumer compiles against and what it runs with.
	for _, variant := range []string{"apiElements", "runtimeElements"} {
		found := false

		for _, candidate := range metadata.Variants {
			if candidate.Name != variant {
				continue
			}

			found = len(candidate.Files) == 1 && candidate.Files[0].Name == name &&
				candidate.Files[0].Size == len(jar) && candidate.Files[0].SHA256 == hex.EncodeToString(digest[:])
		}

		if !found {
			findings = append(findings, fmt.Sprintf("variant %s does not offer exactly %s with the registry JAR's size and SHA-256", variant, name))
		}
	}

	var project struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
	}

	if err := xml.Unmarshal(pom, &project); err != nil {
		return append(findings, "the POM is not XML: "+err.Error())
	}

	if project.GroupID != gradleLibraryGroup || project.ArtifactID != gradleLibraryArtifact || project.Version != version {
		findings = append(findings, fmt.Sprintf("the POM describes %+v, want %s:%s:%s", project, gradleLibraryGroup, gradleLibraryArtifact, version))
	}

	// Gradle's marker in a POM is what sends a Gradle consumer to the module
	// metadata; without it the metadata is published and never read.
	if !strings.Contains(string(pom), "published-with-gradle-metadata") {
		findings = append(findings, "the POM does not refer a Gradle consumer to the module metadata")
	}

	return findings
}

func TestGradleLibraryFixture_IsTwoSelfContainedBuilds(t *testing.T) {
	t.Parallel()

	files, err := gradleLibraryFiles("1.2.3")
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}

	slices.Sort(names)

	want := []string{
		"consumer/build.gradle",
		"consumer/gradle/wrapper/gradle-wrapper.jar",
		"consumer/gradle/wrapper/gradle-wrapper.properties",
		"consumer/gradlew",
		"consumer/settings.gradle",
		"consumer/src/main/java/org/example/fixture/gradleconsumer/Smoke.java",
		"producer/build.gradle",
		"producer/gradle.properties",
		"producer/gradle/wrapper/gradle-wrapper.jar",
		"producer/gradle/wrapper/gradle-wrapper.properties",
		"producer/gradlew",
		"producer/settings.gradle",
		"producer/src/main/java/org/example/fixture/gradlelibrary/Greeting.java",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("the fixture commit holds %v, want %v", names, want)
	}

	for _, finding := range gradleLibraryFixtureFindings(files, "1.2.3") {
		t.Error(finding)
	}
}

// gradleLibraryFixtureFindings checks what the witness rests on and no Gradle
// run here can show: the producer declares the coordinate and a publication
// and no repository of its own, the consumer can resolve from the registry
// alone, the greeting is the producer's, and the distribution is pinned.
func gradleLibraryFixtureFindings(files map[string]string, version string) []string {
	var findings []string

	require := func(file, what string, texts ...string) {
		for _, text := range texts {
			if !strings.Contains(files[file], text) {
				findings = append(findings, fmt.Sprintf("%s: %s (no %q)", file, what, text))
			}
		}
	}
	refuse := func(file, what string, texts ...string) {
		for _, text := range texts {
			if strings.Contains(files[file], text) {
				findings = append(findings, fmt.Sprintf("%s: %s (%q)", file, what, text))
			}
		}
	}

	if files["producer/gradle.properties"] != "version="+version+"\n" {
		findings = append(findings, "producer/gradle.properties: the run's version is not the project's")
	}

	require("producer/build.gradle", "the producer must declare the coordinate and one Java publication",
		"id 'java-library'", "id 'maven-publish'", "group = '"+gradleLibraryGroup+"'", "from components.java")
	refuse("producer/build.gradle", "the product supplies the export repository", "repositories", "url")
	require("producer/settings.gradle", "the project name is the artifact", "rootProject.name = '"+gradleLibraryArtifact+"'")

	greeting := strings.Replace(gradleLibraryGreeting, "fresh consumer", `" + name + "`, 1)
	require("producer/src/main/java/org/example/fixture/gradlelibrary/Greeting.java", "the greeting must be the library's", `"`+greeting+`"`)
	require("consumer/src/main/java/org/example/fixture/gradleconsumer/Smoke.java", "the consumer must run the library's code",
		"import org.example.fixture.gradlelibrary.Greeting;", `Greeting.greet("fresh consumer")`)
	refuse("consumer/src/main/java/org/example/fixture/gradleconsumer/Smoke.java", "the consumer must not spell the greeting itself", "hello")

	require("consumer/build.gradle", "the consumer must name the coordinate and the registry",
		`implementation "`+gradleLibraryGroup+":"+gradleLibraryArtifact+`:${providers.gradleProperty('libraryVersion').get()}"`,
		"url = uri(providers.gradleProperty('registryUrl').get())")
	refuse("consumer/build.gradle", "the registry must be the consumer's only source of the library",
		"mavenCentral", "mavenLocal", "includeBuild", "project(", "files(")
	refuse("consumer/settings.gradle", "the consumer must be a build of its own", "include", "producer")

	if strings.Count(files["consumer/build.gradle"], "maven {") != 1 {
		findings = append(findings, "consumer/build.gradle: want exactly one repository")
	}

	for _, build := range []string{"producer", "consumer"} {
		require(build+"/gradle/wrapper/gradle-wrapper.properties", "the distribution must be pinned by checksum",
			"distributionSha256Sum=", "distributionUrl=https\\://services.gradle.org/distributions/")
	}

	return findings
}

func TestGradleLibraryFixtureFindings_RefuseAnotherFixture(t *testing.T) {
	t.Parallel()

	const (
		greeting = "producer/src/main/java/org/example/fixture/gradlelibrary/Greeting.java"
		smoke    = "consumer/src/main/java/org/example/fixture/gradleconsumer/Smoke.java"
	)

	// Each case replaces old, once, in one file of the fixture.
	for name, tc := range map[string]struct{ file, old, replacement string }{
		"a stale version":               {"producer/gradle.properties", "1.2.3", "1.2.2"},
		"no publication":                {"producer/build.gradle", "from components.java", ""},
		"a producer repository":         {"producer/build.gradle", "publishing {", "publishing {\n    repositories { mavenLocal() }"},
		"another greeting":              {greeting, "hello-gradle-library", "elsewhere"},
		"a consumer that greets itself": {smoke, `Greeting.greet("fresh consumer")`, `"` + gradleLibraryGreeting + `"`},
		"a second repository":           {"consumer/build.gradle", "repositories {", "repositories {\n    mavenLocal()"},
		"a composite build":             {"consumer/settings.gradle", "rootProject.name", "includeBuild '../producer'\nrootProject.name"},
		"an unpinned distribution":      {"consumer/gradle/wrapper/gradle-wrapper.properties", "distributionSha256Sum=", "#"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			files, err := gradleLibraryFiles("1.2.3")
			if err != nil {
				t.Fatal(err)
			}

			changed := strings.Replace(files[tc.file], tc.old, tc.replacement, 1)
			if changed == files[tc.file] {
				t.Fatalf("%s holds no %q, so the case changes nothing", tc.file, tc.old)
			}

			files[tc.file] = changed

			if findings := gradleLibraryFixtureFindings(files, "1.2.3"); len(findings) == 0 {
				t.Fatal("the changed fixture was accepted")
			}
		})
	}
}

// gradleLibraryProbeStubs stands in for everything the job reaches outside
// itself. The product's three phases each check their exact invocation and the
// order they must run in; the consumer's wrapper checks that it runs as a
// stranger in a fresh home, and resolves what STATE says.
const gradleLibraryProbeStubs = `set -eu
export ORIGIN="$PWD"
published=publication/org/example/fixture/hello-gradle-library/1.2.3
keytool() { :; }
curl() {
  test "$*" = "-fsSL -o fixture.tar.gz https://fixture.invalid/fixture/fci-pkggradle/archive/0123abc.tar.gz"
  mkdir -p stage/fci-pkggradle/producer stage/fci-pkggradle/consumer
  : > stage/fci-pkggradle/producer/gradlew
  cat > stage/fci-pkggradle/consumer/gradlew <<'WRAPPER'
#!/bin/sh
set -eu
test -z "${FORGEJO_TOKEN:-}"
test -e "$ORIGIN/deployed"
test "$PWD" = "$ORIGIN/fixture/consumer"
test "$GRADLE_USER_HOME" = "$ORIGIN/consumer-gradle-home"
test -e "$GRADLE_USER_HOME/wrapper/dists/gradle-bin/distribution"
test "$*" = "--no-daemon --quiet run -PregistryUrl=https://fixture.invalid/api/packages/fixture/maven -PlibraryVersion=1.2.3"
published="$ORIGIN/publication/org/example/fixture/hello-gradle-library/1.2.3"
cache="$GRADLE_USER_HOME/caches/modules-2/files-2.1/org.example.fixture/hello-gradle-library/1.2.3"
mkdir -p "$cache/1" "$cache/2"
cp "$published/hello-gradle-library-1.2.3.jar" "$cache/1/"
[ "$STATE" = no-module ] || cp "$published/hello-gradle-library-1.2.3.module" "$cache/2/"
[ "$STATE" != other-bytes ] || printf other > "$cache/1/hello-gradle-library-1.2.3.jar"
if [ "$STATE" = other-output ]; then
  echo 'hello, fresh consumer, from elsewhere'
else
  echo 'hello, fresh consumer, from hello-gradle-library'
fi
WRAPPER
  tar -czf fixture.tar.gz -C stage fci-pkggradle
}
run_product() {
  test "$PWD" = "$ORIGIN"
  case "$1 $2 $3" in
  'build gradle export-publications')
    test "$*" = 'build gradle export-publications --working-dir fixture/producer --output publication --version 1.2.3'
    test -x fixture/producer/gradlew
    test "$GRADLE_USER_HOME" = "$ORIGIN/producer-gradle-home"
    mkdir -p "$GRADLE_USER_HOME/wrapper/dists/gradle-bin" "$published"
    : > "$GRADLE_USER_HOME/wrapper/dists/gradle-bin/distribution"
    for suffix in .pom .module .jar -sources.jar; do
      [ "$STATE$suffix" = no-sources-sources.jar ] || printf '%s' "$suffix" > "$published/hello-gradle-library-1.2.3$suffix"
    done
    ;;
  'release attachments upload')
    test "$*" = 'release attachments upload --tag v0.0.11-pkggradle --pattern gradle-publication.sha256'
    test "$(wc -l < gradle-publication.sha256)" -eq 4
    test ! -e deployed
    ;;
  'publish forge-packages deploy')
    test "$*" = 'publish forge-packages deploy --project-type gradle --working-dir publication --version 1.2.3'
    test -s gradle-publication.sha256
    : > deployed
    ;;
  *)
    return 1
    ;;
  esac
}
[ "$STATE" != stale-home ] || mkdir consumer-gradle-home`

func TestGradleLibraryProbe_RenderedShell(t *testing.T) {
	t.Parallel()

	target := livetest.Target{Forge: provider.ForgeForgejo, Host: "fixture.invalid", Owner: "fixture"}
	body := gradleLibraryProbe(target, gradleLibraryProbeStubs, "fci-pkggradle", "0123abc", "1.2.3", "v0.0.11-pkggradle")
	script := publicationProbeScript(t, provider.ForgeForgejo, body)

	if !strings.Contains(body, "FORGEJO_TOKEN: ${{ secrets.RC_PACKAGE_TOKEN }}") {
		t.Fatal("the job does not publish with the package token")
	}

	for _, shell := range []string{"sh", "bash"} {
		command := exec.CommandContext(t.Context(), shell, "-n")

		command.Stdin = strings.NewReader(script)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s syntax: %v: %s", shell, err, output)
		}
	}

	// Each state but the first is one way the witness must refuse: the export
	// lacks a file the receipt names, the consumer's home is not fresh, or the
	// consumer resolved or ran something other than the producer's library.
	for _, state := range []string{"ready", "no-sources", "stale-home", "other-bytes", "no-module", "other-output"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			err := runProbeShell(t, script, "STATE="+state, "FORGEJO_TOKEN=inert")
			if (err == nil) != (state == "ready") {
				t.Fatalf("state %s: job exit %v", state, err)
			}
		})
	}
}

func TestChecksumReceipt(t *testing.T) {
	t.Parallel()

	one, two := strings.Repeat("a", 64), strings.Repeat("b", 64)

	for name, tc := range map[string]struct {
		receipt string
		ok      bool
	}{
		"both files":         {one + "  lib.pom\n" + two + "  lib.jar\n", true},
		"any order":          {two + "  lib.jar\n" + one + "  lib.pom\n", true},
		"a file missing":     {one + "  lib.pom\n", false},
		"a file twice":       {one + "  lib.pom\n" + one + "  lib.pom\n" + two + "  lib.jar\n", false},
		"an unexpected file": {one + "  lib.pom\n" + two + "  lib.jar\n" + two + "  lib.aar\n", false},
		"a short digest":     {one[:63] + "  lib.pom\n" + two + "  lib.jar\n", false},
		"an upper-case hash": {strings.ToUpper(one) + "  lib.pom\n" + two + "  lib.jar\n", false},
		"empty":              {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			digests, err := checksumReceipt([]byte(tc.receipt), "lib.pom", "lib.jar")
			if (err == nil) != tc.ok {
				t.Fatalf("receipt accepted=%t, want %t: %v", err == nil, tc.ok, err)
			}

			if tc.ok && (digests["lib.pom"] != one || digests["lib.jar"] != two) {
				t.Fatalf("digests %v", digests)
			}
		})
	}
}

func TestGradlePublicationFindings(t *testing.T) {
	t.Parallel()

	jar := []byte("the registry's JAR")
	digest := sha256.Sum256(jar)

	// The shape Gradle writes for a java-library with a sources JAR, reduced
	// to the members the oracle reads.
	const module = `{
  "formatVersion": "1.1",
  "component": {"group": "org.example.fixture", "module": "hello-gradle-library", "version": "1.2.3", "attributes": {}},
  "createdBy": {"gradle": {"version": "9.8.1"}},
  "variants": [
    {"name": "apiElements", "files": [{"name": "hello-gradle-library-1.2.3.jar", "url": "hello-gradle-library-1.2.3.jar", "size": SIZE, "sha256": "SHA256"}]},
    {"name": "runtimeElements", "files": [{"name": "hello-gradle-library-1.2.3.jar", "url": "hello-gradle-library-1.2.3.jar", "size": SIZE, "sha256": "SHA256"}]},
    {"name": "sourcesElements", "files": [{"name": "hello-gradle-library-1.2.3-sources.jar", "size": 1, "sha256": "00"}]}
  ]
}`

	const pom = `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <!-- do_not_remove: published-with-gradle-metadata -->
  <modelVersion>4.0.0</modelVersion>
  <groupId>org.example.fixture</groupId>
  <artifactId>hello-gradle-library</artifactId>
  <version>1.2.3</version>
</project>`

	published := strings.NewReplacer("SIZE", strconv.Itoa(len(jar)), "SHA256", hex.EncodeToString(digest[:])).Replace(module)

	for name, tc := range map[string]struct {
		module, pom string
		jar         []byte
		version     string
		findings    int
	}{
		"the producer's publication":  {published, pom, jar, "1.2.3", 0},
		"another version":             {published, pom, jar, "1.2.4", 4},
		"another JAR in the registry": {published, pom, []byte("the registry's JAr"), "1.2.3", 2},
		"a JAR of another size":       {published, pom, append(slices.Clone(jar), 0), "1.2.3", 2},
		"no runtime variant":          {strings.Replace(published, "runtimeElements", "runtimeOnly", 1), pom, jar, "1.2.3", 1},
		"a second file in a variant": {
			strings.Replace(published, `"sha256": "`+hex.EncodeToString(digest[:])+`"}]`, `"sha256": "`+hex.EncodeToString(digest[:])+`"}, {"name": "other.jar"}]`, 1),
			pom, jar, "1.2.3", 1,
		},
		"another module":         {strings.Replace(published, `"module": "hello-gradle-library"`, `"module": "hello"`, 1), pom, jar, "1.2.3", 1},
		"not created by Gradle":  {strings.Replace(published, `"gradle"`, `"maven"`, 1), pom, jar, "1.2.3", 1},
		"module is not JSON":     {"<project/>", pom, jar, "1.2.3", 1},
		"a POM without a marker": {published, strings.Replace(pom, "published-with-gradle-metadata", "published", 1), jar, "1.2.3", 1},
		"a POM of another group": {published, strings.Replace(pom, "org.example.fixture", "org.example.other", 1), jar, "1.2.3", 1},
		"POM is not XML":         {published, "{}", jar, "1.2.3", 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if findings := gradlePublicationFindings([]byte(tc.module), []byte(tc.pom), tc.jar, tc.version); len(findings) != tc.findings {
				t.Fatalf("%d findings, want %d: %v", len(findings), tc.findings, findings)
			}
		})
	}
}
