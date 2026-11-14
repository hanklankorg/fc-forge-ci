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
	"encoding/xml"
	"fmt"
	"io/fs"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// packageTokenSecret names the repository secret carrying a package-write
// token. Referenced literally in the Forgejo workflow, so it is declared once
// here and the two must agree.
const packageTokenSecret = "RC_PACKAGE_TOKEN"

// PAR-PKG-1: an npm package published through the product appears in the
// forge's own package registry.
func TestInRunner_ForgePackages_NPMPublishReachesTheRegistry(t *testing.T) {
	livetest.RegisterLiveScenario(t)

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
			repo := livetest.NewPublicScratchRepo(t, target, "pkgnpm", livetest.ReadProbeAssetAnonymously)

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
  # GitLab acknowledges uploads before its package/metadata workers finish.
  # Wait for this exact version, not a successful install or a moving dist-tag.
  attempt=0
  until npm view '` + name + `@` + version + `' version --json --registry "$registry" --fetch-retries=0 --fetch-timeout=10000 > version.json 2> metadata.err; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 30 ] || ! grep -Eq '^npm (error|ERR!) code E404$' metadata.err; then
      cat metadata.err >&2
      exit 1
    fi
    sleep 2
  done
  node <<'VERIFY'
const assert = require('node:assert/strict');
assert.equal(require('./version.json'), '` + version + `');
VERIFY
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
)
# A stranger's npm: no token in its environment, an empty configuration and a
# cache of its own, so what it installs came from the public registry.
(
  stranger="$consumer/stranger"
  mkdir "$stranger"
  cd "$stranger"
  printf '%s\n' '{"name":"registry-stranger","version":"1.0.0","private":true}' > package.json
  : > anonymous.npmrc
  env -u NODE_AUTH_TOKEN -u NPM_TOKEN -u FORGEJO_TOKEN -u CI_JOB_TOKEN -u GITHUB_TOKEN -u RELEASE_TOKEN \
    npm_config_userconfig="$stranger/anonymous.npmrc" npm_config_cache="$stranger/cache" \
    npm install --registry "$registry" --ignore-scripts --no-audit --no-fund --package-lock=false --save-exact '` + name + `@` + version + `'
  node <<'VERIFY'
const assert = require('node:assert/strict');
assert.equal(require('` + name + `'), 1);
assert.equal(require('` + name + `/package.json').version, '` + version + `');
VERIFY
)
# A published version is immutable. The second publication carries other
# bytes under the same version: it must be refused because the version exists,
# not for a reason that would refuse any upload, and a fresh install must still
# deliver the first's bytes.
echo "module.exports = 2;" > pkg/index.js
rm -f pkg/*.tgz
( cd pkg && npm pack --silent )
if run_product publish forge-packages deploy --project-type npm --working-dir pkg --npm-tag '` + tagPrefix + `{version}'; then
  echo 'the registry accepted a second publication of ` + name + `@` + version + `' >&2
  exit 1
fi
# npm's own refusal on Forgejo (lab run 2026-11-09), or the registry's status.
if ! grep -Eq 'cannot publish over the previously published versions|E409|E403|EPUBLISHCONFLICT' product-out.txt; then
  echo 'the second publication failed, and not on the version existing' >&2
  exit 1
fi
(
  cd "$consumer"
  export npm_config_userconfig="$consumer/.npmrc"
  export npm_config_cache="$consumer/cache"
  rm -rf node_modules cache
  npm install --registry "$registry" --ignore-scripts --no-audit --no-fund --package-lock=false --save-exact '` + name + `@` + version + `'
  node <<'VERIFY'
const assert = require('node:assert/strict');
assert.equal(require('` + name + `'), 1);
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
mkdir pkg
run_product() {
  test "$PWD" = "$origin"
  if [ "$1 $2 $3" = 'publish forge-packages deploy' ]; then
    test "$METADATA_STATE" != republished || return 0
    : > product-out.txt
    : > refused
    test "$METADATA_STATE" = unavailable || printf '%s\n' 'npm error You cannot publish over the previously published versions: 1.2.3.' > product-out.txt
    return 1
  fi
  test "$1 $2 $3" = 'publish npm write-npmrc'
  test "$4" = --registry
  test "$5" = "$EXPECTED_REGISTRY"
  test "$6" = --output
  : > "$7"
}
npm() {
  test "$PWD" != "$origin"
  if [ "$1" = pack ]; then
    return 0
  fi
  test -f "$npm_config_userconfig"
  test "$npm_config_cache" = "$PWD/cache"
  if [ "$1" = view ] && [ "$3" = version ]; then
    test "$5" = --registry
    test "$6" = "$EXPECTED_REGISTRY"
    test "$7" = --fetch-retries=0
    test "$8" = --fetch-timeout=10000
    attempts=$((attempts + 1))
    if [ "$METADATA_STATE" = auth ]; then
      printf '%s\n' 'npm error code E401' >&2
      return 1
    fi
    if [ "$METADATA_STATE" = missing ] || [ "$attempts" -lt 3 ]; then
      printf '%s\n' 'npm error code E404' >&2
      return 1
    fi
    printf '%s\n' '"1.2.3"'
  elif [ "$1" = install ]; then
    test "$METADATA_STATE" = ready || test "$METADATA_STATE" = republished || test "$METADATA_STATE" = unavailable
    # The first install follows the wait for the version's metadata; the one
    # after the refused publication is a subshell of its own and waits for none.
    # The stranger's install is the one with no token left to it.
    test "$attempts" = 3 || test -f "$origin/refused" || test -z "${FORGEJO_TOKEN-}${CI_JOB_TOKEN-}"
  fi
}
# The stranger runs npm through env, which would start the real one: apply
# what env is told and call the stand-in.
env() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -u) unset "$2"; shift 2 ;;
      *=*) export "$1"; shift ;;
      *) break ;;
    esac
  done
  "$@"
}
sleep() {
  test "$1" = 2
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

			if !strings.Contains(script, "if run_product publish forge-packages deploy --project-type npm --working-dir pkg --npm-tag '"+tagPrefix+"{version}'; then") {
				t.Fatal("the second publication is not the first's deployment")
			}

			// republished is a registry that accepts the same version twice,
			// unavailable one that refuses the second for another reason.
			for _, state := range []string{"ready", "missing", "auth", "republished", "unavailable"} {
				t.Run(state, func(t *testing.T) {
					err := runProbeShell(t, "attempts=0\n"+stub+consume,
						"METADATA_STATE="+state, "EXPECTED_REGISTRY="+registry, "CI_API_V4_URL=https://fixture.invalid/api/v4",
						"CI_PROJECT_ID=123", "CI_JOB_TOKEN=inert", "FORGEJO_TOKEN=inert")
					if (err == nil) != (state == "ready") {
						t.Fatalf("metadata state %s: consumer exit %v", state, err)
					}
				})
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
	livetest.RegisterLiveScenario(t)

	const (
		tag     = "v0.0.9-pkgmvn"
		groupID = "com.example.livetest"
	)

	for _, forge := range livetest.ForgesMeeting(t, forgesClaiming(t, alwaysValidatesTokens, "a package registry"), livetest.NeedsInRunner, livetest.NeedsPackages) {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()
			runMavenDeploy(t, forge, tag, groupID, "fci-parpkg-maven", false)
		})

		// The same library as a development build: one jar module at the
		// root, published with --snapshot as timestamped files.
		t.Run(string(forge)+"-snapshot", func(t *testing.T) {
			t.Parallel()
			runMavenDeploy(t, forge, tag, groupID, "fci-parpkg-maven-snapshot", true)
		})
	}
}

func runMavenDeploy(t *testing.T, forge provider.ForgeAPI, tag, groupID, artifactID string, snapshot bool) {
	t.Helper()

	// Unique and non-prerelease, for the reasons PAR-PKG-1 documents.
	version, scratch := forgePackageVersion(time.Now()), "pkgmvn"
	if snapshot {
		version, scratch = version+"-SNAPSHOT", "pkgmvn-snapshot"
	}

	target := livetest.Accept(t, forge)
	repo := livetest.NewPublicScratchRepo(t, target, scratch, livetest.ReadProbeAssetAnonymously)

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
		mavenDeployProbe(target, assetURL, groupID, artifactID, version, tag, snapshot))
	if conclusion != "success" {
		t.Fatalf("%s: the deploy job concluded %q — `publish forge-packages deploy --project-type maven` did not complete against this forge",
			forge, conclusion)
	}

	after := livetest.PublishedPackageVersions(t, target, repo, "maven", name)
	if !slices.Contains(after, version) {
		t.Errorf("%s: the job succeeded but %s:%s is not in the forge's package registry (found %v) — the deploy reported success without the artifact arriving",
			forge, name, version, after)
	}

	assertMavenBuildPublication(t, target, repo, groupID, artifactID, version, tag, snapshot)
}

// mavenDeployProbe writes a minimal pom and deploys it with the product.
//
// The pom sits at the job root because `publish forge-packages deploy` runs mvn
// in the current directory, which is also where the prelude puts the binary.
func mavenDeployProbe(target livetest.Target, assetURL, groupID, artifactID, version, tag string, snapshot bool) string {
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

	publish := `run_product publish forge-packages deploy --project-type maven --from-build --working-dir . --version ` + version
	if snapshot {
		publish += " --snapshot"
	}

	deploy += publish + "\n"

	// A stranger's Maven: empty settings, no token in its environment and a
	// local repository of its own, so the JAR it resolves came from the public
	// registry and is the one built here.
	registry := `"${FORGEJO_SERVER_URL}/api/packages/` + target.Owner + `/maven"`
	if target.Forge == provider.ForgeGitLab {
		registry = `"${CI_API_V4_URL}/projects/${CI_PROJECT_ID}/packages/maven"`
	}

	deploy += `echo '<settings/>' > anonymous-settings.xml
env -u FORGEJO_TOKEN -u CI_JOB_TOKEN mvn -B --no-transfer-progress -s anonymous-settings.xml -gs anonymous-settings.xml \
  -Dmaven.repo.local="$PWD/anonymous-repository" dependency:get \
  -Dartifact=` + groupID + `:` + artifactID + `:` + version + ` -Dtransitive=false -DremoteRepositories=forge::default::` + registry + `
cmp target/` + artifactID + `-` + version + `.jar anonymous-repository/` + strings.ReplaceAll(groupID, ".", "/") + `/` + artifactID + `/` + version + `/` + artifactID + `-` + version + `.jar
`

	// Forgejo refuses a second upload of a release file. GitLab's registry
	// accepts duplicates unless a group setting forbids them, and the product
	// has no check of its own, so there the refusal is not the product's to
	// show. The second attempt carries a different POM: had it been accepted,
	// the registry's POM would no longer be the one the receipt names.
	// A snapshot is published again by design, under a new timestamp.
	if target.Forge == provider.ForgeForgejo && !snapshot {
		deploy += `printf '<!-- a second publication -->\n' >> pom.xml
if ` + publish + `; then
  echo 'the registry accepted a second publication of ` + artifactID + ` ` + version + `' >&2
  exit 1
fi
# The registry's own answer as the product reports it (lab run 2026-11-10),
# so no other failure reads as the refusal.
if ! grep -qF 'maven registry upload returned HTTP 409' product-out.txt; then
  echo 'the second publication failed, and not on the version existing' >&2
  exit 1
fi
`
	}

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
// result and local repository. A snapshot's files are read under the
// timestamped names its registry metadata gives each of the four.
func assertMavenBuildPublication(t *testing.T, target livetest.Target, repo, group, artifact, version, tag string, snapshot bool) {
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

	if snapshot {
		var metadata struct {
			Versions []struct {
				Extension  string `xml:"extension"`
				Classifier string `xml:"classifier"`
				Value      string `xml:"value"`
			} `xml:"versioning>snapshotVersions>snapshotVersion"`
		}
		if err := xml.Unmarshal(livetest.MavenRegistryFile(t, target, repo, base+"maven-metadata.xml"), &metadata); err != nil {
			t.Fatal(err)
		}

		for path, name := range files {
			suffix, timestamped := strings.TrimPrefix(name, stem), ""

			for _, entry := range metadata.Versions {
				classified := "." + entry.Extension
				if entry.Classifier != "" {
					classified = "-" + entry.Classifier + classified
				}

				if classified == suffix && !strings.Contains(entry.Value, "SNAPSHOT") {
					timestamped = artifact + "-" + entry.Value + suffix
				}
			}

			if timestamped == "" {
				t.Fatalf("the registry's snapshot metadata names no timestamped %s", suffix)
			}

			files[path] = timestamped
		}
	}

	for line := range strings.SplitSeq(strings.TrimSpace(string(receipt)), "\n") {
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
