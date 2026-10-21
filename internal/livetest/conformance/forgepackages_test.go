// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

// PAR-PKG-*: `publish forge-packages deploy` against the forges' own package
// registries.
//
// An entire command family with no live coverage until now. Both adapters
// implement the resolver roles, and every test underneath this one checks the
// resolved URL and auth scheme against a fake written from the same assumptions
// as the adapter — which is precisely the tie only a real server can break, and
// the registries disagree in ways that make it worth breaking:
//
//   - GitLab deploys to <CI_API_V4_URL>/projects/<id>/packages/... and
//     authenticates with a Job-Token header;
//   - Forgejo deploys to <server>/api/packages/<owner>/... and authenticates
//     with an `Authorization: token` header;
//   - Forgejo requires npm packages to be scoped to the owner; GitLab accepts
//     any name.
//
// On the tier boundary: docs/testing.md keeps toolchain claims in the black-box
// suite, and that rule is respected here. npm is not what is under test — it is
// the transport, the way curl is the transport elsewhere in this tier. The
// claim is a forge-API claim: that what the adapter resolved is what the
// registry accepts. The npm fixture also checks exact-version consumption and
// the publication tag; package presence is independently read from the forge.
// This is fixture primitive coverage, not API-linter upstream publish acceptance.
//
// The publish must happen inside a job because GitLab's registry credential is
// $CI_JOB_TOKEN, which exists only while the job runs.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// packageTokenSecret names the repository secret carrying a package-write
// token. Referenced literally in the Forgejo workflow, so it is declared once
// here and the two must agree.
const packageTokenSecret = "RC_PACKAGE_TOKEN"

// PAR-PKG-1: an npm package published through the product appears in the
// forge's own package registry.
func TestInRunner_ForgePackages_NPMPublishReachesTheRegistry(t *testing.T) {
	const (
		tag  = "v0.0.8-pkgnpm"
		base = "fci-parpkg"
	)

	// Unique per run. The explicit version-specific tag is required by the
	// publication policy for stable versions and prereleases alike.
	//
	// Unique because the Gitea family scopes packages to the owner, not to the
	// repository: a fresh scratch repo does not give a fresh registry, so a fixed
	// version would collide with the previous run and be refused.
	version := forgePackageVersion(time.Now())

	for _, forge := range livetest.ForgesMeeting(t, forgesClaiming(t, alwaysValidatesTokens, "a package registry"), livetest.NeedsInRunner) {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()

			target := livetest.Accept(t, forge)
			repo := livetest.NewScratchRepo(t, target, "pkgnpm")

			livetest.PrepareTag(t, target, repo, tag)
			livetest.PublishBinaryAsset(t, target, repo, tag, t.TempDir())

			assetURL := livetest.ReleaseAssetURL(t, target, repo, tag, "forge-ci")
			name := livetest.NPMPackageName(target, base)

			// Forgejo's automatic job token is refused by the package registry
			// (measured: a PUT with it answers 401/403), so the job is given a
			// token that carries write:package — which is what a real Forgejo
			// pipeline has to do too. GitLab needs none: its job token already
			// carries package-write for the project that issued it.
			if forge == provider.ForgeForgejo {
				livetest.SetRepoSecret(t, target, repo, packageTokenSecret, target.Token)
			}

			// The package outlives the scratch repository on the Gitea family,
			// so it is swept explicitly rather than left for the repo delete.
			t.Cleanup(func() {
				livetest.DeletePublishedPackage(t, target, repo, "npm", name, version)
			})

			// A fresh scratch repository each run, so the package cannot already
			// be there — but assert it anyway, because "it was published" and "it
			// was already published" are the same observation afterwards.
			if before := livetest.PublishedPackageVersions(t, target, repo, "npm", name); slices.Contains(before, version) {
				t.Fatalf("%s: %s@%s exists before publishing, so finding it afterwards would prove nothing",
					forge, name, version)
			}

			conclusion := livetest.RunWorkflow(t, target, repo, "forge-packages-npm",
				npmPublishProbe(target, assetURL, name, version))
			if conclusion != "success" {
				t.Fatalf("%s: the publish/consume job concluded %q — npm publication, exact-version installation, runtime content or dist-tag verification failed",
					forge, conclusion)
			}

			// The forge is the oracle, not npm: the tool that published is the
			// worst possible witness that publishing worked.
			after := livetest.PublishedPackageVersions(t, target, repo, "npm", name)
			if !slices.Contains(after, version) {
				t.Errorf("%s: the job succeeded but %s@%s is not in the forge's package registry (found %v) — the deploy reported success without the package arriving",
					forge, name, version, after)
			}
		})
	}
}

// npmPublishProbe publishes a minimal package and consumes its exact version.
//
// The job runs in a node image because the product shells out to npm for this
// ecosystem; that is the product's design, not the scenario's choice. The job
// checks registry consumption; Go independently checks package presence afterward.
func npmPublishProbe(target livetest.Target, assetURL, name, version string) string {
	return npmPublishWorkflow(target, livetest.ProbePrelude(target, assetURL), name, version)
}

// Keep rendering separate from the accepted target's CA/binary setup so the
// exact workflow sent to a forge can also be checked without a lab contract.
func npmPublishWorkflow(target livetest.Target, prelude, name, version string) string {
	tagPrefix := "release-"
	if target.Forge == provider.ForgeForgejo {
		tagPrefix = "release."
	}
	// Single-quoted heredoc: the package manifest must reach disk verbatim,
	// without the shell touching anything inside it.
	//
	// No strict-ssl relaxation: the prelude has already installed the
	// environment's CA, so npm verifies the registry the ordinary way and this
	// scenario now covers the trust path instead of stepping around it.
	manifest := `mkdir -p pkg
cat > pkg/package.json <<'MANIFEST'
{
  "name": "` + name + `",
  "version": "` + version + `",
  "description": "livetest fixture for PAR-PKG-1",
  "main": "index.js",
  "license": "EUPL-1.2"
}
MANIFEST
echo "module.exports = 1;" > pkg/index.js

# The product publishes the packed tarball, so pack it first.
( cd pkg && npm pack --silent )
ls -l pkg`

	// Reuse the publishing job's credential without printing it or putting it on
	// argv. The product's publish config is temporary, so render a consumer config
	// with the existing placeholder-based helper. Isolate both install and cache
	// from the packed source; the exact version must come back from the registry.
	auth := `registry="` + target.BaseURL() + `/api/packages/` + target.Owner + `/npm/"
export NODE_AUTH_TOKEN="$FORGEJO_TOKEN"`
	if target.Forge == provider.ForgeGitLab {
		auth = `registry="${CI_API_V4_URL}/projects/${CI_PROJECT_ID}/packages/npm/"
export NODE_AUTH_TOKEN="$CI_JOB_TOKEN"`
	}
	consume := `set +x
set -eu
` + auth + `
consumer="$(mktemp -d)"
trap 'rm -rf "$consumer"' EXIT
run_product publish npm write-npmrc --registry "$registry" --output "$consumer/.npmrc"
(
  cd "$consumer"
  printf '%s\n' '{"name":"registry-consumer","version":"1.0.0","private":true}' > package.json
  export npm_config_userconfig="$consumer/.npmrc"
  export npm_config_cache="$consumer/cache"
  npm install --registry "$registry" --ignore-scripts --no-audit --no-fund --package-lock=false --save-exact '` + name + `@` + version + `'
  node <<'VERIFY'
const assert = require('node:assert/strict');
assert.equal(require('` + name + `'), 1);
assert.equal(require('` + name + `/package.json').version, '` + version + `');
VERIFY
  npm view '` + name + `@` + version + `' dist-tags --json --registry "$registry" > dist-tags.json
  node <<'VERIFY'
const assert = require('node:assert/strict');
assert.equal(require('./dist-tags.json')['` + tagPrefix + version + `'], '` + version + `');
VERIFY
)`

	if target.Forge == provider.ForgeGitLab {
		return `publish:
  image: node:24
  script:
    - |
      ` + indent(prelude, 6) + `
      ` + indent(manifest, 6) + `
      ` + indent(`run_product publish forge-packages deploy --project-type npm --working-dir pkg --npm-tag 'release-{version}'`, 6) + `
      ` + indent(consume, 6) + `
`
	}

	return `on: [push]
jobs:
  publish:
    runs-on: ubuntu-latest
    container:
      image: node:24
    steps:
      - name: publish an npm package to this forge's own registry
        env:
          # Overrides the automatic token, which the package registry refuses.
          FORGEJO_TOKEN: ${{ secrets.RC_PACKAGE_TOKEN }}
        run: |
          ` + indent(prelude, 10) + `
          ` + indent(manifest, 10) + `
          ` + indent(`run_product publish forge-packages deploy --project-type npm --working-dir pkg --npm-tag 'release.{version}'`, 10) + `
          ` + indent(consume, 10) + `
`
}

func TestNPMPublishWorkflow_RenderedShell(t *testing.T) {
	t.Parallel()

	for _, forge := range []provider.ForgeAPI{provider.ForgeGitLab, provider.ForgeForgejo} {
		t.Run(string(forge), func(t *testing.T) {
			target := livetest.Target{Forge: forge, Host: "fixture.invalid", Owner: "fixture"}
			body := npmPublishWorkflow(target, "set -eu", "@fixture/package", "1.2.3")
			script := publicationProbeScript(t, forge, body)
			if !strings.Contains(script, "npm view '@fixture/package@1.2.3' dist-tags --json") {
				t.Fatal("consumption tag query must select the installed version explicitly")
			}
			for _, shell := range []string{"sh", "bash"} {
				command := exec.CommandContext(t.Context(), shell, "-n")
				command.Stdin = strings.NewReader(script)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("%s syntax: %v: %s", shell, err, output)
				}
			}
			tagPrefix := "release-"
			if forge == provider.ForgeForgejo {
				tagPrefix = "release."
			}
			if !strings.Contains(script, "run_product publish forge-packages deploy --project-type npm --working-dir pkg --npm-tag '"+tagPrefix+"{version}'") {
				t.Fatal("deployment escaped the YAML script")
			}
			if !strings.Contains(script, "assert.equal(require('./dist-tags.json')['"+tagPrefix+"1.2.3'], '1.2.3');") {
				t.Fatal("dist-tag assertion does not match the selected publication tag")
			}

			// Execute the generated consumer shell with inert transports. The
			// product wrapper deliberately requires the original directory, like
			// ProbePrelude's ./forge-ci. npm and node must run in the fresh one.
			_, consume, ok := strings.Cut(script, "set +x\n")
			if !ok {
				t.Fatal("missing consumption script")
			}
			stub := `set -eu
origin="$PWD"
run_product() {
  test "$PWD" = "$origin"
  test "$1 $2 $3" = 'publish npm write-npmrc'
  test "$4" = --registry
  test "$5" = "$EXPECTED_REGISTRY"
  test "$6" = --output
  : > "$7"
}
npm() {
  test "$PWD" != "$origin"
  test -f "$npm_config_userconfig"
  test "$npm_config_cache" = "$PWD/cache"
}
node() {
  test "$PWD" != "$origin"
  cat > /dev/null
}
`
			registry := "https://fixture.invalid/api/packages/fixture/npm/"
			if forge == provider.ForgeGitLab {
				registry = "https://fixture.invalid/api/v4/projects/123/packages/npm/"
			}
			if err := runProbeShell(t, stub+consume,
				"EXPECTED_REGISTRY="+registry, "CI_API_V4_URL=https://fixture.invalid/api/v4",
				"CI_PROJECT_ID=123", "CI_JOB_TOKEN=inert", "FORGEJO_TOKEN=inert"); err != nil {
				t.Fatalf("consumer working-directory/config boundary: %v", err)
			}
		})
	}
}

// PAR-PKG-2: a Maven artifact published through the product appears in the
// forge's own package registry.
//
// Not a repeat of PAR-PKG-1 in another ecosystem: the Maven half is where the
// two forges' auth schemes actually diverge. The product renders a settings.xml
// carrying an HTTP header, and which header differs — GitLab wants
// `Job-Token: <token>`, Forgejo wants `Authorization: token <token>`. A wrong
// scheme is indistinguishable from a right one until a real registry answers,
// and no fake we write can disagree with the assumption it was written from.
//
// This scenario costs more than the others: `mvn deploy` resolves its plugins
// from Maven Central the first time, so the job reaches the public internet for
// its toolchain. That is the same category as pulling the job image — toolchain
// provisioning, not a registry under test. Every registry this asserts against
// is the lab's own.
func TestInRunner_ForgePackages_MavenDeployReachesTheRegistry(t *testing.T) {
	const (
		tag        = "v0.0.9-pkgmvn"
		groupID    = "com.example.livetest"
		artifactID = "fci-parpkg-maven"
	)

	// Unique and non-prerelease, for the reasons PAR-PKG-1 documents.
	version := forgePackageVersion(time.Now())

	for _, forge := range livetest.ForgesMeeting(t, forgesClaiming(t, alwaysValidatesTokens, "a package registry"), livetest.NeedsInRunner, livetest.NeedsPackages) {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()

			target := livetest.Accept(t, forge)
			repo := livetest.NewScratchRepo(t, target, "pkgmvn")

			livetest.PrepareTag(t, target, repo, tag)
			livetest.PublishBinaryAsset(t, target, repo, tag, t.TempDir())

			assetURL := livetest.ReleaseAssetURL(t, target, repo, tag, "forge-ci")
			name := livetest.MavenPackageName(target, groupID, artifactID)

			if forge == provider.ForgeForgejo {
				livetest.SetRepoSecret(t, target, repo, packageTokenSecret, target.Token)
			}

			t.Cleanup(func() {
				livetest.DeletePublishedPackage(t, target, repo, "maven", name, version)
			})

			if before := livetest.PublishedPackageVersions(t, target, repo, "maven", name); slices.Contains(before, version) {
				t.Fatalf("%s: %s:%s exists before deploying, so finding it afterwards would prove nothing",
					forge, name, version)
			}

			conclusion := livetest.RunWorkflow(t, target, repo, "forge-packages-maven",
				mavenDeployProbe(target, assetURL, groupID, artifactID, version, tag))
			if conclusion != "success" {
				t.Fatalf("%s: the deploy job concluded %q — `publish forge-packages deploy --project-type maven` did not complete against this forge",
					forge, conclusion)
			}

			after := livetest.PublishedPackageVersions(t, target, repo, "maven", name)
			if !slices.Contains(after, version) {
				t.Errorf("%s: the job succeeded but %s:%s is not in the forge's package registry (found %v) — the deploy reported success without the artifact arriving",
					forge, name, version, after)
			}
			assertMavenBuildPublication(t, target, repo, groupID, artifactID, version, tag)
		})
	}
}

// mavenDeployProbe writes a minimal pom and deploys it with the product.
//
// The pom sits at the job root because `publish forge-packages deploy` runs mvn
// in the current directory, which is also where the prelude puts the binary.
func mavenDeployProbe(target livetest.Target, assetURL, groupID, artifactID, version, tag string) string {
	pom := `cat > pom.xml <<'POM'
<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>` + groupID + `</groupId>
  <artifactId>` + artifactID + `</artifactId>
  <version>` + version + `</version>
  <packaging>jar</packaging>
  <properties>
    <maven.compiler.release>21</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
    <project.build.outputTimestamp>2026-10-20T00:00:00Z</project.build.outputTimestamp>
  </properties>
  <build><plugins>
    <plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-compiler-plugin</artifactId><version>3.14.1</version></plugin>
    <plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-jar-plugin</artifactId><version>3.4.2</version></plugin>
    <plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-deploy-plugin</artifactId><version>2.8.2</version></plugin>
  </plugins></build>
  <profiles><profile>
    <id>library-attachments</id>
    <build><plugins>
      <plugin>
        <groupId>org.apache.maven.plugins</groupId><artifactId>maven-source-plugin</artifactId><version>3.3.1</version>
        <executions><execution><id>sources</id><goals><goal>jar-no-fork</goal></goals></execution></executions>
      </plugin>
      <plugin>
        <groupId>org.apache.maven.plugins</groupId><artifactId>maven-javadoc-plugin</artifactId><version>3.11.2</version>
        <configuration><notimestamp>true</notimestamp></configuration>
        <executions><execution><id>javadoc</id><goals><goal>jar</goal></goals></execution></executions>
      </plugin>
    </plugins></build>
  </profile></profiles>
</project>
POM
mkdir -p src/main/java
cat > src/main/java/Demo.java <<'JAVA'
/** A real compiled library, with source and generated API documentation. */
public class Demo {
  /** @return the fixture value */
  public static int one() { return 1; }
}
JAVA`

	// The JVM does not read /etc/ssl/certs, so the prelude's work does not reach
	// Maven: it keeps its own truststore and has to be told separately. Importing
	// the same CA is what lets the deploy verify the registry properly, and it
	// replaces a set of maven.wagon.* insecure flags that also forced the
	// resolver onto the wagon transport — the native transport ignores those
	// properties, so they looked like they relaxed TLS without doing it, and the
	// deploy failed with a PKIX error that read like a product problem.
	//
	// -cacerts targets the JDK's own store (JDK 9+); changeit is its default
	// password, unchanged in these images.
	const trustInJVM = `keytool -importcert -noprompt -alias forge-lab \
  -cacerts -storepass changeit -file ` + livetest.LabCAPath

	// -B for non-interactive output.
	// Freeze build hashes before deploy: Maven deploy reruns the lifecycle. Merely
	// finding classifier files in target would not prove that deploy attached them,
	// or that the registry received the bytes built before publication.
	deploy := `mvn -B --no-transfer-progress package -Plibrary-attachments
sha256sum pom.xml target/` + artifactID + `-` + version + `.jar target/` + artifactID + `-` + version + `-sources.jar target/` + artifactID + `-` + version + `-javadoc.jar > build-digests.sha256
`
	// GitLab's job token can publish packages and job artifacts, but cannot use
	// the project uploads API backing release attachments. Keep that boundary.
	if target.Forge == provider.ForgeForgejo {
		deploy += "run_product release attachments upload --tag " + tag + " --pattern build-digests.sha256\n"
	}
	deploy += `run_product publish forge-packages deploy --project-type maven --from-build --working-dir . --version ` + version

	if target.Forge == provider.ForgeGitLab {
		return `publish:
  image: maven:3.9-eclipse-temurin-21
  artifacts:
    paths: [build-digests.sha256]
  script:
    - |
      ` + indent(livetest.ProbePrelude(target, assetURL), 6) + `
      ` + indent(pom, 6) + `
      ` + indent(trustInJVM, 6) + `
      ` + indent(deploy, 6) + `
`
	}

	return `on: [push]
jobs:
  publish:
    runs-on: ubuntu-latest
    container:
      image: maven:3.9-eclipse-temurin-21
    steps:
      - name: deploy a Maven artifact to this forge's own registry
        env:
          FORGEJO_TOKEN: ${{ secrets.RC_PACKAGE_TOKEN }}
        run: |
          ` + indent(livetest.ProbePrelude(target, assetURL), 10) + `
          ` + indent(pom, 10) + `
          ` + indent(trustInJVM, 10) + `
          ` + indent(deploy, 10) + `
`
}

// The oracle reads the pre-deploy receipt from the retained job artifact or
// release asset and actual registry files, independently of Maven's deploy
// result and local repository.
func assertMavenBuildPublication(t *testing.T, target livetest.Target, repo, group, artifact, version, tag string) {
	t.Helper()
	var receiptURL string
	if target.Forge == provider.ForgeGitLab {
		receiptURL = target.BaseURL() + "/api/v4/projects/" + url.PathEscape(target.Owner+"/"+repo) + "/jobs/artifacts/main/raw/build-digests.sha256?job=publish"
	} else {
		receiptURL = livetest.ReleaseAssetURL(t, target, repo, tag, "build-digests.sha256")
	}
	receipt := livetest.Download(t, target, receiptURL, 4096)
	stem := artifact + "-" + version
	files := map[string]string{
		"pom.xml":                         stem + ".pom",
		"target/" + stem + ".jar":         stem + ".jar",
		"target/" + stem + "-sources.jar": stem + "-sources.jar",
		"target/" + stem + "-javadoc.jar": stem + "-javadoc.jar",
	}
	base := strings.ReplaceAll(group, ".", "/") + "/" + artifact + "/" + version + "/"
	for _, line := range strings.Split(strings.TrimSpace(string(receipt)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			t.Fatal("invalid pre-deploy checksum receipt")
		}
		name, ok := files[fields[1]]
		if !ok {
			t.Fatalf("unexpected or duplicate build receipt path %q", fields[1])
		}
		delete(files, fields[1])
		body := livetest.MavenRegistryFile(t, target, repo, base+name)
		if fmt.Sprintf("%x", sha256.Sum256(body)) != fields[0] {
			t.Fatalf("registry %s differs from the pre-deploy build", name)
		}
		if strings.HasSuffix(name, ".jar") {
			entry := "Demo.class"
			if strings.HasSuffix(name, "-sources.jar") {
				entry = "Demo.java"
			} else if strings.HasSuffix(name, "-javadoc.jar") {
				entry = "Demo.html"
			}
			archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fs.Stat(archive, entry); err != nil {
				t.Fatalf("registry %s lacks %s: %v", name, entry, err)
			}
		}
	}
	if len(files) != 0 {
		t.Fatalf("pre-deploy receipt omits %d publication files", len(files))
	}
	t.Log("registry main/POM/sources/Javadoc match all four pre-deploy build hashes")
}
