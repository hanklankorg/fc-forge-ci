// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// versionBumpLib stands in for the helper checkout's library: artifact upload
// and download move a named directory through one shared store, as the
// forge's artifact service does between jobs, and every other call runs the
// engine of this tree.
const versionBumpLib = `run_forge_ci() {
  case "$1 $2" in
    "artifact upload") mkdir -p -- "${ARTIFACT_STORE:?}"; rm -rf -- "${ARTIFACT_STORE}/$4"; cp -a -- "$6" "${ARTIFACT_STORE}/$4" ;;
    "artifact download")
      if [ ! -d "${ARTIFACT_STORE:?}/$4" ]; then echo "no artifact $4" >&2; return 1; fi
      mkdir -p -- "$6"; cp -a -- "${ARTIFACT_STORE}/$4/." "$6/" ;;
    *) "${FORGE_CI_ENGINE:?}" "$@" ;;
  esac
}
`

// The declaration of the consumer the tests release: a Maven service, an npm
// web app and a Go tool, which has no version file.
const versionBumpDeclaration = `artifacts:
  - name: api
    project-type: maven
    working-directory: services/api
  - name: web
    project-type: npm
    working-directory: web
  - name: tool
    project-type: go
    working-directory: tool
`

const (
	versionBumpPOM     = "<project>\n  <modelVersion>4.0.0</modelVersion>\n  <groupId>se.digg</groupId>\n  <artifactId>api</artifactId>\n  <version>%s</version>\n</project>\n"
	versionBumpPackage = "{\n  \"name\": \"web\",\n  \"version\": \"%s\"\n}\n"
	versionBumpLock    = "{\n  \"name\": \"web\",\n  \"version\": \"%s\",\n  \"lockfileVersion\": 3,\n  \"requires\": true,\n" +
		"  \"packages\": {\n    \"\": {\n      \"name\": \"web\",\n      \"version\": \"%s\"\n    }\n  }\n}\n"
	// The file patterns bump-plan reports for each family's projects.
	versionBumpJavaPattern = "services/api/CHANGELOG.md :(glob)services/api/**/pom.xml"
	versionBumpNodePattern = "web/CHANGELOG.md web/package.json web/package-lock.json"
)

// versionBumpFiles are the consumer's committed files with its projects at
// version, keyed by path.
func versionBumpFiles(version string) map[string]string {
	return map[string]string{
		"services/api/pom.xml":    strings.ReplaceAll(versionBumpPOM, "%s", version),
		"web/package.json":        strings.ReplaceAll(versionBumpPackage, "%s", version),
		"web/package-lock.json":   strings.ReplaceAll(versionBumpLock, "%s", version),
		"tool/go.mod":             "module example.invalid/tool\n\ngo 1.26\n",
		"README.md":               "# Example\n",
		"CHANGELOG.md":            "# Changelog\n",
		".forge-ci/artifacts.yml": versionBumpDeclaration,
	}
}

// versionBumpProject reports whether path is a project file a version job
// changes.
func versionBumpProject(path string) bool {
	return strings.HasPrefix(path, "services/") || strings.HasPrefix(path, "web/")
}

// versionBumpCheckout is a consumer checkout at the release request: its
// files with the projects at 0.1.9 committed with real Git, the helper
// library beside them untracked, and the project's own Git configuration set
// against a readable patch. declared false leaves the declaration out.
func versionBumpCheckout(t *testing.T, declared bool) *sandbox {
	t.Helper()

	box := newSandbox(t, "prepare-release")
	box.write(".forge-ci-helpers/.engine/bin/lib-forge-ci.sh", versionBumpLib, 0o600)

	tracked := []string{}

	for path, body := range versionBumpFiles("0.1.9") {
		if path == ".forge-ci/artifacts.yml" && !declared {
			continue
		}

		box.write(path, body, 0o600)
		tracked = append(tracked, path)
	}

	for _, arguments := range [][]string{
		{"init", "--quiet", "--initial-branch=main"},
		{"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"},
		{"config", "commit.gpgsign", "false"},
		append([]string{"add", "--"}, tracked...),
		{"commit", "--quiet", "--message", "chore: seed"},
		// A project's configuration may ask for other prefixes and colour.
		{"config", "diff.noprefix", "true"}, {"config", "color.diff", "always"},
	} {
		out, err := git(t, box.root, arguments...)
		require.NoError(t, err, out)
	}

	return box
}

// versionBumpEnv is what every prepare-release step here runs with.
func versionBumpEnv(t *testing.T, store string) []string {
	t.Helper()

	return []string{"ARTIFACT_STORE=" + store, "FORGE_CI_ENGINE=" + forgeCIEngine(t), "RUNNER_TEMP=" + t.TempDir(), "RELEASE_POLICY=stable"}
}

// The plan job names one version job a family whose projects need their
// native tool, each in the image the caller gave the family: the Maven and
// npm projects, not the Go tool. A planned family without its image stops
// the release before any version job, and a request without a declaration
// plans no version job.
func TestVersionPlanNamesOneJobPerFamilyItsDeclarationNeeds(t *testing.T) {
	t.Parallel()

	script := workflowStep(t, "prepare-release.yml", "plan", "Plan the version jobs").Run
	env := versionBumpEnv(t, t.TempDir())
	java := "RUNTIME_IMAGE_JAVA=registry.example/java@sha256:" + strings.Repeat("1", 64)
	node := "RUNTIME_IMAGE_NODE=registry.example/node@sha256:" + strings.Repeat("2", 64)

	planned := versionBumpCheckout(t, true)
	got := planned.run(script, append(env, java, node)...)
	require.Equal(t, 0, got.status, got.stderr)
	require.Equal(t, `versions=[{"family":"java","project-type":"maven"},{"family":"node","project-type":"npm"}]`+"\n", planned.output())

	got = planned.run(script, append(env, java)...)
	require.NotEqual(t, 0, got.status)
	require.Contains(t, got.stderr, "the plan has node projects, which need the runtime-image-node input")
	require.Empty(t, planned.output())

	undeclared := versionBumpCheckout(t, false)
	got = undeclared.run(script, env...)
	require.Equal(t, 0, got.status, got.stderr)
	require.Equal(t, "versions=[]\n", undeclared.output())
}

// uploadVersionPatch runs a family's version job on a fresh checkout: the
// project files as the native tools leave them, beside a stray change to a
// tracked file and an untracked file, then its upload step with the file
// pattern bump-plan reports for the family. It returns the reported digest.
func uploadVersionPatch(t *testing.T, env []string, family, pattern string) string {
	t.Helper()

	box := versionBumpCheckout(t, true)

	for path, body := range versionBumpFiles("0.2.0") {
		if versionBumpProject(path) {
			box.write(path, body, 0o600)
		}
	}

	box.write("README.md", "# Example, edited by a build plugin\n", 0o600)
	box.write("stray.txt", "left behind\n", 0o600)

	got := box.run(workflowStep(t, "prepare-release.yml", "version", "Upload the version patch").Run,
		append(env, "FAMILY="+family, "FILE_PATTERN="+pattern)...)
	require.Equal(t, 0, got.status, got.stderr)

	digest, found := strings.CutPrefix(strings.TrimSpace(box.output()), "patch_digest_"+family+"=")
	require.True(t, found, box.output())

	return digest
}

// versionPatch reads a family's uploaded patch from the artifact store.
func versionPatch(t *testing.T, store, family string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(store, "release-version-"+family, "version-bump.patch"))
	require.NoError(t, err)

	return string(body)
}

// Each family's job hands on the diff of its own version files alone, with
// its digest; the signing job checks each against its job's digest, joins
// them, and the engine applies the joined patch and writes the changelog
// copies. What it names is the release commit: with real Git, staging those
// paths beside CHANGELOG.md leaves the rendered commit message, the helper
// checkout and every other file out.
func TestVersionJobsHandTheSigningJobADigestCheckedJoinedPatch(t *testing.T) {
	t.Parallel()

	store := filepath.Join(t.TempDir(), "artifacts")
	env := versionBumpEnv(t, store)

	javaDigest := uploadVersionPatch(t, env, "java", versionBumpJavaPattern)
	nodeDigest := uploadVersionPatch(t, env, "node", versionBumpNodePattern)

	java, node := versionPatch(t, store, "java"), versionPatch(t, store, "node")
	require.Equal(t, fileSHA256(t, filepath.Join(store, "release-version-java", "version-bump.patch")), javaDigest)
	require.Equal(t, fileSHA256(t, filepath.Join(store, "release-version-node", "version-bump.patch")), nodeDigest)
	require.Contains(t, java, "--- a/services/api/pom.xml\n+++ b/services/api/pom.xml\n")
	require.Contains(t, node, "--- a/web/package.json\n+++ b/web/package.json\n")
	require.Contains(t, node, "--- a/web/package-lock.json\n+++ b/web/package-lock.json\n")

	for _, patch := range []string{java, node} {
		require.NotContains(t, patch, "\x1b[")
		require.NotContains(t, patch, "README.md")
		require.NotContains(t, patch, "stray.txt")
	}

	require.NotContains(t, java, "web/")
	require.NotContains(t, node, "services/")

	// The signing job: the rendered files laid into a fresh checkout.
	sign := versionBumpCheckout(t, true)
	sign.write("CHANGELOG.md", "# Changelog\n\n## 0.2.0\n", 0o600)
	sign.write("commit-msg.txt", "chore(release): 0.2.0\n", 0o600)

	got := sign.run(workflowStep(t, "prepare-release.yml", "prepare", "Verify and apply the version bump").Run, append(env,
		"PATCH_DIGEST_JAVA="+javaDigest, "PATCH_DIGEST_NODE="+nodeDigest, "RELEASE_TAG=v0.2.0")...)
	require.Equal(t, 0, got.status, got.stderr)

	for path, body := range versionBumpFiles("0.2.0") {
		if versionBumpProject(path) {
			require.Equal(t, body, readSandboxFile(t, sign, path), path)
		}
	}

	pathsFile, found := strings.CutPrefix(strings.TrimSpace(sign.output()), "paths-file=")
	require.True(t, found, sign.output())

	body, err := os.ReadFile(pathsFile)
	require.NoError(t, err)

	paths := strings.Fields(string(body))
	require.ElementsMatch(t, []string{
		"services/api/pom.xml", "services/api/CHANGELOG.md", "web/package.json", "web/package-lock.json",
		"web/CHANGELOG.md", "tool/CHANGELOG.md",
	}, paths)

	out, err := git(t, sign.root, append([]string{"add", "--", "CHANGELOG.md"}, paths...)...)
	require.NoError(t, err, out)

	staged, err := git(t, sign.root, "diff", "--cached", "--name-only")
	require.NoError(t, err)
	require.ElementsMatch(t, append([]string{"CHANGELOG.md"}, paths...), strings.Fields(staged))

	untracked, err := git(t, sign.root, "status", "--porcelain", "--untracked-files=normal", "--", "commit-msg.txt", ".forge-ci-helpers")
	require.NoError(t, err)
	require.Equal(t, "?? .forge-ci-helpers/\n?? commit-msg.txt", untracked)
}

// A patch that differs from the digest its job reported, a planned family
// whose job reported none, and a linked patch each stop the signing job
// before anything is applied: the project files stay as the request has them.
func TestVersionBumpSigningJobRefusesAPatchItsJobDidNotReport(t *testing.T) {
	t.Parallel()

	store := filepath.Join(t.TempDir(), "artifacts")
	env := versionBumpEnv(t, store)
	javaDigest := uploadVersionPatch(t, env, "java", versionBumpJavaPattern)
	nodeDigest := uploadVersionPatch(t, env, "node", versionBumpNodePattern)

	linked := filepath.Join(t.TempDir(), "artifacts")
	require.NoError(t, os.CopyFS(linked, os.DirFS(store)))
	require.NoError(t, os.Remove(filepath.Join(linked, "release-version-node", "version-bump.patch")))
	require.NoError(t, os.Symlink(filepath.Join(store, "release-version-node", "version-bump.patch"),
		filepath.Join(linked, "release-version-node", "version-bump.patch")))

	script := workflowStep(t, "prepare-release.yml", "prepare", "Verify and apply the version bump").Run

	for name, scenario := range map[string]struct {
		env     []string
		refusal string
	}{
		"a digest its job did not report": {
			env:     []string{"PATCH_DIGEST_JAVA=" + strings.Repeat("0", 64), "PATCH_DIGEST_NODE=" + nodeDigest},
			refusal: "the java version patch differs from its job's digest",
		},
		"a planned family without a digest": {
			env:     []string{"PATCH_DIGEST_JAVA=" + javaDigest},
			refusal: `the plan bumps the node family, but "Prepare: version node" reported no patch digest`,
		},
		"a linked patch": {
			env:     []string{"ARTIFACT_STORE=" + linked, "PATCH_DIGEST_JAVA=" + javaDigest, "PATCH_DIGEST_NODE=" + nodeDigest},
			refusal: "the node version artifact must contain a regular version-bump.patch",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			box := versionBumpCheckout(t, true)
			got := box.run(script, append(append(slicesClone(env), scenario.env...), "RELEASE_TAG=v0.2.0")...)
			require.NotEqual(t, 0, got.status)
			require.Contains(t, got.stderr, scenario.refusal)

			for path, body := range versionBumpFiles("0.1.9") {
				require.Equal(t, body, readSandboxFile(t, box, path), path)
			}

			require.NotContains(t, box.output(), "paths-file=")
		})
	}
}

// slicesClone copies values, so each scenario appends to its own.
func slicesClone(values []string) []string { return append([]string(nil), values...) }

// A request without a declaration plans no bump: the version job's empty row
// uploads nothing, and the signing job applies nothing and names no path, so
// the release commit carries the changelog alone.
func TestVersionBumpSigningJobGoesOnWithoutAPlannedBump(t *testing.T) {
	t.Parallel()

	box := versionBumpCheckout(t, false)
	box.write("CHANGELOG.md", "# Changelog\n\n## 0.2.0\n", 0o600)

	got := box.run(workflowStep(t, "prepare-release.yml", "prepare", "Verify and apply the version bump").Run,
		append(versionBumpEnv(t, filepath.Join(t.TempDir(), "empty")), "RELEASE_TAG=v0.2.0")...)
	require.Equal(t, 0, got.status, got.stderr)
	require.NotContains(t, box.output(), "paths-file=")

	status, err := git(t, box.root, "status", "--porcelain", "--untracked-files=no")
	require.NoError(t, err)
	require.Equal(t, "M CHANGELOG.md", status)
}
