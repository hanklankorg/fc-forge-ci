// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestReleaseArtifactsAreReadableWithoutOpeningPrivateFiles(t *testing.T) {
	t.Parallel()

	// Each producer job and the hand-off directory it leaves for the next.
	for job, dir := range map[string]string{
		"Prepare: changelog":     "_release-render",
		"Prepare: version java":  "_release-bump-java",
		"Prepare: version node":  "_release-bump-node",
		"Prepare: version rust":  "_release-bump-rust",
		"Build: java":            "_release-build-java",
		"Build: node":            "_release-build-node",
		"Build: go":              "_release-build-go",
		"Build: rust":            "_release-build-rust",
		"Build: android":         "_release-build-android",
		"Build: release files":   "_release-build",
		"Images: plan":           "_release-images-plan",
		"Images: build amd64":    "_release-images-amd64",
		"Images: build arm64":    "_release-images-arm64",
		"Images: publish":        "_release-images",
		"Sign: files and images": "_release-sign",
		"Publish: images":        "_release-promote",
	} {
		t.Run(job, func(t *testing.T) {
			t.Parallel()

			workspace := t.TempDir()
			handoff := filepath.Join(workspace, dir)
			private := filepath.Join(workspace, "private")
			incoming := filepath.Join(workspace, "incoming")

			for _, dir := range []string{handoff, private, incoming} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			for _, file := range []string{filepath.Join(handoff, "payload"), filepath.Join(private, "key"), filepath.Join(incoming, "payload")} {
				if err := os.WriteFile(file, []byte("fixture bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if err := os.Symlink(private, filepath.Join(handoff, "private-link")); err != nil {
				t.Fatal(err)
			}

			runReleaseAfterScript(t, workspace, job)
			requireMode(t, handoff, 0o755)
			requireMode(t, filepath.Join(handoff, "payload"), 0o644)
			requireMode(t, private, 0o700)
			requireMode(t, filepath.Join(private, "key"), 0o600)
			requireMode(t, incoming, 0o700)
			requireMode(t, filepath.Join(incoming, "payload"), 0o600)

			if body, err := os.ReadFile(filepath.Join(handoff, "payload")); err != nil || string(body) != "fixture bytes" {
				t.Fatal("artifact publication changed its bytes")
			}
		})
	}
}

func TestReleaseArtifactsNeverFollowASymlinkedOutputRoot(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	private := filepath.Join(t.TempDir(), "private")

	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(private, filepath.Join(workspace, "_release-sign")); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(private, "key"), []byte("private fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	runReleaseAfterScript(t, workspace, "Sign: files and images")
	requireMode(t, private, 0o700)
	requireMode(t, filepath.Join(private, "key"), 0o600)
}

// runReleaseAfterScript runs a job's after_script as GitLab runs it, named
// another job by a project variable, which must not move its hand-off.
func runReleaseAfterScript(t *testing.T, workspace, job string) {
	t.Helper()

	body := releaseJobsBodyFor(t, "buildah")

	script := joinedScript(gitlabrender.ResolveExtends(t, body, body[job].(map[string]any))["after_script"])
	if script == "" {
		t.Fatal("release artifact publication hook is missing")
	}

	ctx := commandContext(t)

	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c", script)
	command.Dir = workspace
	command.Env = []string{"PATH=" + componentTools(t), "HOME=" + workspace, "CI_PROJECT_DIR=" + workspace,
		"CI_JOB_NAME=Publish: release", "rc_handoff=private", "CI_JOB_STATUS=failed"}

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("artifact hook failed: %v\n%s", err, output)
	}
}

func requireMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode=%#o, want %#o", path, info.Mode().Perm(), mode)
	}
}

// "Build: release files" gathers exactly the planned build jobs' hand-offs: a
// planned family that handed nothing on, or files from a family the plan
// does not build, stops the release before the declaring build runs.
func TestReleaseFilesGatherExactlyThePlannedFamilies(t *testing.T) {
	t.Parallel()

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")

	for name, scenario := range map[string]struct {
		handoffs []string
		refusal  string
	}{
		"as planned":                  {handoffs: []string{"java"}},
		"a planned build is gone":     {refusal: `the plan builds the java family, but "Build: java" handed nothing on`},
		"an unplanned family's files": {handoffs: []string{"java", "node"}, refusal: `"Build: node" handed files on, but the plan builds no node family`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			for _, family := range scenario.handoffs {
				if err := os.Mkdir(filepath.Join(dir, "_release-build-"+family), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			ctx := commandContext(t)

			command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-eu", "-c",
				`source "$1"; rc_engine() { printf 'engine %s\n' "$*"; }; rc_planned_jobs() { printf 'Prepare: version java\nBuild: java\n' >"$1"; }; rc_gather_families`,
				"gather", script)
			command.Env = []string{"PATH=/usr/bin:/bin", "CI_PROJECT_DIR=" + dir, "RC_WORK=" + t.TempDir()}

			out, err := command.CombinedOutput()
			if scenario.refusal == "" {
				if err != nil || !strings.Contains(string(out), "engine release gather-exports") {
					t.Fatalf("gather refused the planned hand-offs: %v\n%s", err, out)
				}

				return
			}

			if err == nil || !strings.Contains(string(out), scenario.refusal) || strings.Contains(string(out), "gather-exports") {
				t.Fatalf("gather went on: %v\n%s", err, out)
			}
		})
	}
}

// packNPMPackage writes the packed tarball of an npm package.
func packNPMPackage(t *testing.T, path, name, version string) {
	t.Helper()

	var packed bytes.Buffer

	compressed := gzip.NewWriter(&packed)
	archive := tar.NewWriter(compressed)
	body := []byte(`{"name":"` + name + `","version":"` + version + `"}`)

	if err := archive.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o600, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}

	if _, err := archive.Write(body); err != nil {
		t.Fatal(err)
	}

	if err := errors.Join(archive.Close(), compressed.Close(), os.WriteFile(path, packed.Bytes(), 0o600)); err != nil {
		t.Fatal(err)
	}
}

// packagesEngine runs the engine's package binding for real and records the
// rest: a deploy as the files of its working directory, and whether that is
// a directory of the job's own.
const packagesEngine = `rc_engine() {
if [ "$3" = npm-tarballs ]; then OWNER_CLI_HELPER=1 "$OWNER_CLI_BINARY" -test.run='^TestOwnerCLI$' -- "$@"; return; fi
if [ "$3" != deploy ]; then echo "engine: $*"; return; fi
case "$7" in "$RC_WORK"/package.*) ;; *) echo not-staged ;; esac
printf 'deploy %s %s %s\n' "$(ls "$7")" "$8" "$9"
}; `

// "Publish: npm packages" publishes exactly the planned projects' packages
// of the signed dist, each alone in a directory of its own and under the
// release's tag: a planned package that is gone, a tarball the plan does not
// send, another package or version in a planned one's place, a link, a job
// the plan did not name or a job without the package token stops it before
// the registry is written. The build's hand-off refuses the same dist before
// anything is signed.
func TestReleasePublishesExactlyThePlannedPackages(t *testing.T) {
	t.Parallel()

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")

	const planned = `packages/ui\n.\n`

	as := map[string][2]string{"ui.tgz": {"@fixture/ui", "1.2.3"}, "lib.tgz": {"lib", "1.2.3"}}

	for name, scenario := range map[string]struct {
		planned  string
		tarballs map[string][2]string
		linked   bool
		noToken  bool
		refusal  string
	}{
		"as planned":           {planned: planned, tarballs: as},
		"a package is gone":    {planned: planned, tarballs: map[string][2]string{"lib.tgz": as["lib.tgz"]}, refusal: "packs @fixture/ui@1.2.3"},
		"an unplanned tarball": {planned: planned, tarballs: map[string][2]string{"ui.tgz": as["ui.tgz"], "lib.tgz": as["lib.tgz"], "other.tgz": {"other", "1.2.3"}}, refusal: "no npm project the plan publishes"},
		"another version":      {planned: planned, tarballs: map[string][2]string{"ui.tgz": {"@fixture/ui", "1.2.2"}, "lib.tgz": as["lib.tgz"]}, refusal: "packs @fixture/ui@1.2.3"},
		"another package":      {planned: planned, tarballs: map[string][2]string{"ui.tgz": {"@fixture/else", "1.2.3"}, "lib.tgz": as["lib.tgz"]}, refusal: "packs @fixture/ui@1.2.3"},
		"a linked tarball":     {planned: planned, tarballs: map[string][2]string{"lib.tgz": as["lib.tgz"]}, linked: true, refusal: "confined regular file"},
		"no package planned":   {tarballs: as, refusal: "the plan sends no npm package"},
		"no package token":     {planned: planned, tarballs: as, noToken: true, refusal: "RELEASE_PACKAGE_TOKEN"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "packages", "ui"), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.Mkdir(filepath.Join(dir, "dist"), 0o700); err != nil {
				t.Fatal(err)
			}

			for file, content := range map[string]string{
				"dist/release-notes.md":    "data",
				"package.json":             `{"name":"lib","version":"1.2.3"}`,
				"packages/ui/package.json": `{"name":"@fixture/ui","version":"1.2.3"}`,
			} {
				if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			for file, identity := range scenario.tarballs {
				packNPMPackage(t, filepath.Join(dir, "dist", file), identity[0], identity[1])
			}

			if scenario.linked {
				if err := os.Symlink("lib.tgz", filepath.Join(dir, "dist", "ui.tgz")); err != nil {
					t.Fatal(err)
				}
			}

			run := func(function string) (string, error) {
				command := exec.CommandContext(commandContext(t), componentBash(t), "--noprofile", "--norc", "-eu", "-c",
					`source "$1"; `+packagesEngine+`rc_release_checkout() { :; }; rc_read_handoff() { export DIST_DIR=dist; }; `+
						`rc_planned_packages() { printf '`+scenario.planned+`' >"$1"; }; `+function,
					"packages", script)
				command.Dir = dir
				command.Env = []string{"PATH=/usr/bin:/bin", "CI_PROJECT_DIR=" + t.TempDir(), "RC_WORK=" + t.TempDir(), "RELEASE_VERSION=1.2.3", "OWNER_CLI_BINARY=" + os.Args[0]}

				if !scenario.noToken {
					command.Env = append(command.Env, "RELEASE_PACKAGE_TOKEN=inert-canary-value")
				}

				out, err := command.CombinedOutput()

				return string(out), err
			}

			// Before anything is signed. The build job holds no package
			// token, and a release that plans no package may carry any file.
			built, buildErr := run("rc_build_handoff")
			if refused := scenario.refusal != "" && scenario.planned != "" && !scenario.noToken; refused != !strings.Contains(built, "engine: release files validate") ||
				(refused && (buildErr == nil || !strings.Contains(built, scenario.refusal))) {
				t.Fatalf("the build's hand-off: %v\n%s", buildErr, built)
			}

			out, err := run("rc_publish_packages")
			if scenario.refusal == "" {
				if err != nil || out != "deploy ui.tgz --npm-tag release-1.2.3\ndeploy lib.tgz --npm-tag release-1.2.3\n" {
					t.Fatalf("each planned package was not published once, alone and under the release's tag: %v\n%s", err, out)
				}

				return
			}

			if err == nil || !strings.Contains(out, scenario.refusal) || strings.Contains(out, "deploy ") {
				t.Fatalf("publishing went on: %v\n%s", err, out)
			}
		})
	}
}
