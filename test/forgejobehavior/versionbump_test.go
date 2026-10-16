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
// and download move a directory through one shared store, as the forge's
// artifact service does between two jobs, and every other call runs the
// engine of this tree.
const versionBumpLib = `run_forge_ci() {
  case "$1 $2" in
    "artifact upload") rm -rf -- "${ARTIFACT_STORE:?}"; cp -a -- "$6" "${ARTIFACT_STORE}" ;;
    "artifact download") mkdir -p -- "$6"; cp -a -- "${ARTIFACT_STORE:?}/." "$6/" ;;
    *) "${FORGE_CI_ENGINE:?}" "$@" ;;
  esac
}
`

const versionBumpPOM = "<project>\n  <modelVersion>4.0.0</modelVersion>\n  <groupId>se.digg</groupId>\n" +
	"  <artifactId>app</artifactId>\n  <version>%s</version>\n</project>\n"

// versionBumpCheckout is a consumer checkout at the release request: a
// committed POM, the helper library beside it, and the project's own Git
// configuration set against a readable patch.
func versionBumpCheckout(t *testing.T) *sandbox {
	t.Helper()

	box := newSandbox(t, "prepare-release")
	box.write(".forge-ci-helpers/.forgejo/bin/lib-forge-ci.sh", versionBumpLib, 0o600)
	box.write("pom.xml", strings.ReplaceAll(versionBumpPOM, "%s", "0.1.9"), 0o600)

	for _, arguments := range [][]string{
		{"init", "--quiet", "--initial-branch=main"},
		{"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"},
		{"add", "pom.xml"}, {"commit", "--quiet", "--message", "chore: seed"},
		// A project's configuration may ask for other prefixes and colour.
		{"config", "diff.noprefix", "true"}, {"config", "color.diff", "always"},
	} {
		out, err := git(t, box.root, arguments...)
		require.NoError(t, err, out)
	}

	return box
}

// The bump job's patch, carried by the artifact store, is verified and
// applied by the signing job's step with the engine of this tree: the POM ends
// at the release version, and the step names it for the release commit.
func TestVersionBumpRoundTripsFromTheKeyFreeJobToTheSigningJob(t *testing.T) {
	t.Parallel()

	store := filepath.Join(t.TempDir(), "artifact")
	env := []string{"ARTIFACT_STORE=" + store, "FORGE_CI_ENGINE=" + forgeCIEngine(t), "RUNNER_TEMP=" + t.TempDir()}

	bump := versionBumpCheckout(t)
	// What the bump step's `mvn versions:set` leaves behind.
	bump.write("pom.xml", strings.ReplaceAll(versionBumpPOM, "%s", "0.2.0"), 0o600)

	uploaded := bump.run(workflowStep(t, "prepare-release.yml", "version-bump", "Upload the version-bump patch").Run, env...)
	require.Equal(t, 0, uploaded.status, uploaded.stderr)

	digest, found := strings.CutPrefix(strings.TrimSpace(bump.output()), "digest=")
	require.True(t, found, bump.output())
	require.Equal(t, fileSHA256(t, filepath.Join(store, "version-bump.patch")), digest)

	patch, err := os.ReadFile(filepath.Join(store, "version-bump.patch"))
	require.NoError(t, err)
	require.Contains(t, string(patch), "--- a/pom.xml\n+++ b/pom.xml\n")
	require.NotContains(t, string(patch), "\x1b[")

	sign := versionBumpCheckout(t)
	applied := sign.run(workflowStep(t, "prepare-release.yml", "prepare", "Verify and apply the version bump").Run, append(env,
		"PATCH_DIGEST="+digest, "PROJECT_TYPE=maven", "RELEASE_TAG=v0.2.0", "WORKING_DIRECTORY=.")...)
	require.Equal(t, 0, applied.status, applied.stderr)
	require.Equal(t, strings.ReplaceAll(versionBumpPOM, "%s", "0.2.0"), readSandboxFile(t, sign, "pom.xml"))

	pathsFile, found := strings.CutPrefix(strings.TrimSpace(sign.output()), "paths-file=")
	require.True(t, found, sign.output())

	paths, err := os.ReadFile(pathsFile)
	require.NoError(t, err)
	require.Equal(t, "pom.xml\n", string(paths))

	// The same patch under a digest its job did not report is refused, and
	// the POM is left as it was.
	refused := versionBumpCheckout(t)
	wrong := refused.run(workflowStep(t, "prepare-release.yml", "prepare", "Verify and apply the version bump").Run, append(env,
		"PATCH_DIGEST="+strings.Repeat("0", 64), "PROJECT_TYPE=maven", "RELEASE_TAG=v0.2.0", "WORKING_DIRECTORY=.")...)
	require.NotEqual(t, 0, wrong.status)
	require.Contains(t, wrong.stderr, "does not match the digest")
	require.Equal(t, strings.ReplaceAll(versionBumpPOM, "%s", "0.1.9"), readSandboxFile(t, refused, "pom.xml"))
}

// A bump that changed no tracked file uploads nothing: a release that asked
// for a bump must carry one.
func TestVersionBumpRefusesAnEmptyBump(t *testing.T) {
	t.Parallel()

	box := versionBumpCheckout(t)
	store := filepath.Join(t.TempDir(), "artifact")

	got := box.run(workflowStep(t, "prepare-release.yml", "version-bump", "Upload the version-bump patch").Run,
		"ARTIFACT_STORE="+store, "FORGE_CI_ENGINE=/nonexistent", "RUNNER_TEMP="+t.TempDir())
	require.NotEqual(t, 0, got.status)
	require.Contains(t, got.stderr, "changed no tracked file")
	require.NoDirExists(t, store)
}

// A linked patch in the artifact never reaches the verifier.
func TestVersionBumpRefusesALinkedPatch(t *testing.T) {
	t.Parallel()

	box := versionBumpCheckout(t)
	store := filepath.Join(t.TempDir(), "artifact")
	require.NoError(t, os.MkdirAll(store, 0o700))
	require.NoError(t, os.Symlink(filepath.Join(box.root, "pom.xml"), filepath.Join(store, "version-bump.patch")))

	got := box.run(workflowStep(t, "prepare-release.yml", "prepare", "Verify and apply the version bump").Run,
		"ARTIFACT_STORE="+store, "FORGE_CI_ENGINE=/nonexistent", "RUNNER_TEMP="+t.TempDir(),
		"PATCH_DIGEST="+strings.Repeat("0", 64), "PROJECT_TYPE=maven", "RELEASE_TAG=v0.2.0", "WORKING_DIRECTORY=.")
	require.NotEqual(t, 0, got.status)
	require.Contains(t, got.stderr, "must contain a regular version-bump.patch")
}
