// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The signer's tag checks, one in each privileged job.
var signerTagChecks = []struct{ job, step string }{
	{"sign", "Verify release tag points to release SHA"},
	{"promote", "Verify release tag before image promotion"},
	{"publish", "Verify release tag before publish"},
}

// signerChange is what a prepared release commit does beside the changelog:
// files to write with their modes, links to create, and every path to stage.
type signerChange struct {
	files map[string]string
	modes map[string]os.FileMode
	links map[string]string
}

// planned is the change a version job hands over for versionBumpDeclaration:
// its Maven and npm projects at 0.2.0.
func planned() signerChange {
	change := signerChange{files: map[string]string{}}

	for path, body := range versionBumpFiles("0.2.0") {
		if versionBumpProject(path) {
			change.files[path] = body
		}
	}

	return change
}

// with returns the change with the further files written.
func (c signerChange) with(files map[string]string) signerChange {
	maps.Copy(c.files, files)

	return c
}

// signerRelease is a consumer repository at a prepared release: the request
// commit, the versionBumpCheckout files (the default declaration only when
// declared) and seed, tagged release-request/v0.2.0, and its single child,
// tagged v0.2.0, with the changelog and change, pushed to a bare remote. It returns the sandbox, the
// request and the release commit.
func signerRelease(t *testing.T, declared bool, seed map[string]string, change signerChange) (*sandbox, string, string) {
	t.Helper()

	box := versionBumpCheckout(t, declared)
	box.write("helpers/.engine/bin/lib-forge-ci.sh", versionBumpLib, 0o600)

	run := func(steps ...[]string) {
		for _, arguments := range steps {
			out, err := git(t, box.root, arguments...)
			require.NoError(t, err, out)
		}
	}
	commit := func(message string, paths []string) {
		run(append([]string{"add", "--"}, paths...), []string{"commit", "--quiet", "--message", message})
	}

	seeded := make([]string, 0, len(seed))

	for path, body := range seed {
		box.write(path, body, 0o600)
		seeded = append(seeded, path)
	}

	if len(seeded) > 0 {
		commit("chore: seed the release request", seeded)
	}

	run([]string{"tag", "--annotate", "--message", "request", "release-request/v0.2.0"})

	box.write("CHANGELOG.md", "# Changelog\n\n## v0.2.0\n", 0o600)

	staged := make([]string, 0, 1+len(change.files)+len(change.modes)+len(change.links))
	staged = append(staged, "CHANGELOG.md")

	for path, body := range change.files {
		box.write(path, body, 0o600)
		staged = append(staged, path)
	}

	for path, mode := range change.modes {
		require.NoError(t, os.Chmod(filepath.Join(box.root, path), mode))
		staged = append(staged, path)
	}

	for path, target := range change.links {
		require.NoError(t, os.Remove(filepath.Join(box.root, path)))
		require.NoError(t, os.Symlink(target, filepath.Join(box.root, path)))
		staged = append(staged, path)
	}

	commit("chore(release): v0.2.0", staged)
	run([]string{"tag", "--annotate", "--message", "release", "v0.2.0"},
		[]string{"init", "--quiet", "--bare", filepath.Join(box.root, "server", "origin.git")},
		[]string{"push", "--quiet", filepath.Join(box.root, "server", "origin.git"), "main", "--tags"})

	request, err := git(t, box.root, "rev-parse", "release-request/v0.2.0^{commit}")
	require.NoError(t, err, request)

	release, err := git(t, box.root, "rev-parse", "HEAD")
	require.NoError(t, err, release)

	return box, strings.TrimSpace(request), strings.TrimSpace(release)
}

// signerTagEnv is what a signer tag check runs with, the caller's
// artifacts-config as the coordinates job validated it.
func signerTagEnv(t *testing.T, box *sandbox, request, release, config string) []string {
	t.Helper()

	return []string{
		"CONSUMER_WORKSPACE=" + box.root, "HELPERS_DIR=" + filepath.Join(box.root, "helpers"),
		"FORGE_CI_ENGINE=" + forgeCIEngine(t), "RUNNER_TEMP=" + t.TempDir(),
		"FORGEJO_SERVER=" + filepath.Join(box.root, "server"), "FORGEJO_REPO=origin",
		"RELEASE_TAG=v0.2.0", "RELEASE_SHA=" + release, "RELEASE_REQUEST_SHA=" + request, "ARTIFACTS_CONFIG=" + config,
	}
}

// Every privileged signer job admits a release commit that changes the
// changelog and exactly the version bump its declaration plans, and refuses
// one that changes anything else beside them.
func TestSignerTagChecksAdmitExactlyThePlannedBump(t *testing.T) {
	t.Parallel()

	for _, check := range signerTagChecks {
		script := workflowStep(t, "sign-and-publish-release.yml", check.job, check.step).Run

		box, request, release := signerRelease(t, true, nil, planned())
		got := box.run(script, signerTagEnv(t, box, request, release, "")...)
		require.Equal(t, 0, got.status, "%s: %s", check.job, got.stderr)

		box, request, release = signerRelease(t, true, nil, planned().with(map[string]string{"README.md": "# Example, changed\n"}))
		got = box.run(script, signerTagEnv(t, box, request, release, "")...)
		require.NotEqual(t, 0, got.status, "%s admitted a stray README.md change", check.job)
		require.Contains(t, got.stderr, "must change only CHANGELOG.md and the planned version bumps", check.job)
	}
}

// The signer refuses what a name match alone would admit: a release commit
// that edits the declaration to plan another project and its bump, a bump
// checked against another declaration than the one the release planned, a
// text version file made executable beside its bump, and a changelog that
// is executable or a link.
func TestSignerTagCheckRefusesWhatThePlanDoesNotAdmit(t *testing.T) {
	t.Parallel()

	script := workflowStep(t, "sign-and-publish-release.yml", "sign", "Verify release tag points to release SHA").Run

	// A Gradle project bumped as text, declared beside the others.
	const gradle = "  - name: app\n    project-type: gradle\n    working-directory: app\n"

	gradleSeed := map[string]string{".forge-ci/artifacts.yml": versionBumpDeclaration + gradle, "app/gradle.properties": "version=0.1.9\n"}
	gradleBump := planned().with(map[string]string{"app/gradle.properties": "version=0.2.0\n"})

	// The declaration under another name, and no default one.
	renamedSeed := map[string]string{".forge-ci/release.yml": versionBumpDeclaration}

	for name, scenario := range map[string]struct {
		undeclared bool // the request has no default declaration
		seed       map[string]string
		change     signerChange
		config     string
		want       string // empty: admitted
	}{
		"a text bump at its mode":     {seed: gradleSeed, change: gradleBump},
		"a text bump made executable": {seed: gradleSeed, change: signerChange{files: gradleBump.files, modes: map[string]os.FileMode{"app/gradle.properties": 0o755}}, want: "holds more than the version bump"},
		"the declaration edited to plan another project": {
			change: planned().with(map[string]string{".forge-ci/artifacts.yml": versionBumpDeclaration + gradle, "app/gradle.properties": "version=0.2.0\n"}),
			want:   "must change only CHANGELOG.md and the planned version bumps",
		},
		"the release's other declaration": {undeclared: true, seed: renamedSeed, change: planned(), config: ".forge-ci/release.yml"},
		"a declaration the release did not plan from": {
			undeclared: true, seed: renamedSeed, change: planned(),
			want: "prepared release must change only CHANGELOG.md;",
		},
		"an executable changelog": {change: signerChange{files: planned().files, modes: map[string]os.FileMode{"CHANGELOG.md": 0o755}}, want: "must be a regular file at its parent's mode"},
		"a linked changelog":      {change: signerChange{files: planned().files, links: map[string]string{"CHANGELOG.md": "README.md"}}, want: "is not one regular file"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			box, request, release := signerRelease(t, !scenario.undeclared, scenario.seed, scenario.change)

			got := box.run(script, signerTagEnv(t, box, request, release, scenario.config)...)
			if scenario.want == "" {
				require.Equal(t, 0, got.status, got.stderr)

				return
			}

			require.NotEqual(t, 0, got.status, "admitted")
			require.Contains(t, got.stderr, scenario.want)
		})
	}
}

// The signer's preflight hands on a repository-relative artifacts-config,
// emits nothing for the default, and refuses a path that leaves the
// checkout before any signer job starts.
func TestSignerPreflightAdmitsOnlyARelativeDeclaration(t *testing.T) {
	t.Parallel()

	script := workflowStep(t, "sign-and-publish-release.yml", "coordinates", "Validate trust coordinates").Run
	env := []string{
		"HELPERS_REPOSITORY_INPUT=owner/forge-ci", "SERVER_URL=https://forge.example", "TRANSPARENCY_LOG_INPUT=none",
		"SIGNER_IMAGE_INPUT=forge.example/owner/signer@sha256:" + strings.Repeat("a", 64),
	}

	for config, want := range map[string]string{"": "", ".forge-ci/release.yml": "artifacts_config=.forge-ci/release.yml\n"} {
		box := newSandbox(t, "coordinates")
		got := box.run(script, append(env, "ARTIFACTS_CONFIG_INPUT="+config)...)
		require.Equal(t, 0, got.status, got.stderr)
		require.True(t, strings.HasPrefix(box.output(), want+"helpers_repository="), "artifacts-config %q: %q", config, box.output())
	}

	for _, config := range []string{"/etc/artifacts.yml", "../artifacts.yml", "a/../../artifacts.yml", "-x"} {
		box := newSandbox(t, "coordinates")
		got := box.run(script, append(env, "ARTIFACTS_CONFIG_INPUT="+config)...)
		require.NotEqual(t, 0, got.status, "artifacts-config %q admitted", config)
		require.Contains(t, got.stderr, "artifacts-config must be a repository-relative path")
	}
}
