// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// These tests drive prepare-live.sh and demo-checkout.sh offline. A fake go
// records the preflight's argv and refuses; a fake just records each call's
// argv. Which images a prepare builds is tested in
// test/live/internal/livetest; these pin what the script refuses before any
// effect.

const fakePrepareGo = `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_GO_ARGV"
exit 2
`

const fakePrepareJust = `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_JUST_ARGV"
`

// prepareLane is one prepared directory and the fakes to run the script on
// it.
type prepareLane struct {
	dir, workDir, argvFile, goArgvFile, envFile string
	env                                         []string
}

func newPrepareLane(t *testing.T) prepareLane {
	t.Helper()

	fakeDir, state := testfs.NewReal(t).Root, testfs.NewReal(t).Root
	writeExecutable(t, filepath.Join(fakeDir, "go"), fakePrepareGo)
	writeExecutable(t, filepath.Join(fakeDir, "just"), fakePrepareJust)

	lane := prepareLane{
		dir:        filepath.Join(state, "prepared"),
		workDir:    testfs.NewReal(t).Root,
		argvFile:   filepath.Join(state, "just-argv"),
		goArgvFile: filepath.Join(state, "go-argv"),
		envFile:    filepath.Join(state, "just-env"),
	}
	if err := os.MkdirAll(lane.dir, 0o700); err != nil {
		t.Fatal(err)
	}

	lane.env = []string{
		"PATH=" + fakeDir + string(os.PathListSeparator) +
			testenv.GNUPath(t, "bash", "sed", "tail", "tee", "date", "mktemp", "rm", "dirname", "wc", "cat") +
			string(os.PathListSeparator) + testenv.ToolPath(t, "git"),
		"HOME=" + testfs.NewReal(t).Root,
		"TMPDIR=" + testfs.NewReal(t).Root,
		"LC_ALL=C",
		"FAKE_GO_ARGV=" + lane.goArgvFile,
		"FAKE_JUST_ARGV=" + lane.argvFile,
		"FAKE_JUST_ENV=" + lane.envFile,
	}

	return lane
}

func runScript(t *testing.T, dir string, env []string, script string, args ...string) (string, int) {
	t.Helper()

	path, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), liveCommandBound)
	defer cancel()

	command := exec.CommandContext(ctx, testBash(t), append([]string{path}, args...)...)
	command.Env, command.Dir = env, dir

	var errOut strings.Builder

	command.Stderr = &errOut
	err = command.Run()

	var exit *exec.ExitError

	switch {
	case err == nil:
		return errOut.String(), 0
	case errors.As(err, &exit):
		return errOut.String(), exit.ExitCode()
	default:
		t.Fatalf("run %s: %v", script, err)
	}

	return "", 0
}

func readLines(t *testing.T, path string) []string {
	t.Helper()

	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		t.Fatal(err)
	}

	return strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
}

func TestImagePreparationResourceRefusalPreservesDestinations(t *testing.T) {
	for _, script := range []string{"prepare-live.sh", "publish-live-image.sh"} {
		t.Run(script, func(t *testing.T) {
			lane := newPrepareLane(t)

			sentinel := filepath.Join(lane.dir, "prepared.env")
			if err := os.WriteFile(sentinel, []byte("preserve\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			lab := filepath.Join(t.TempDir(), "lab with spaces")
			if err := os.Mkdir(lab, 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(lab, "justfile"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			// NUL argv receipt, no real compiler, engine or publisher.
			fake := filepath.Join(t.TempDir(), "go")
			writeExecutable(t, fake, "#!/usr/bin/env bash\nprintf '%s\\0' \"$@\" >\"$FAKE_GO_ARGV\"\nexit 2\n")
			env := append(slices.Clone(lane.env), "GO="+fake, "FORGE_CI_IMAGE_BUILD_BUDGET=required")

			args := []string{lab, lane.dir, "forgejo", "build-budget=required", "build-cpus=2; literal"}
			if script == "publish-live-image.sh" {
				args = []string{"signer", lab, sentinel}
			}

			_, status := runScript(t, lane.workDir, env, script, args...)
			if status != 2 {
				t.Fatalf("status %d", status)
			}

			body, err := os.ReadFile(sentinel)
			if err != nil || string(body) != "preserve\n" {
				t.Fatal("refusal changed destination")
			}

			if calls := readLines(t, lane.argvFile); len(calls) != 0 {
				t.Fatalf("publisher calls %q", calls)
			}

			argv, err := os.ReadFile(lane.goArgvFile)
			if err != nil {
				t.Fatal(err)
			}

			if script == "prepare-live.sh" && !strings.Contains(string(argv), "build-cpus=2; literal\x00") {
				t.Fatalf("lost token boundary %q", argv)
			}
		})
	}
}

func TestImageArchiveRequiredResourcesRefuseBeforeEffects(t *testing.T) {
	just, err := exec.LookPath("just")
	if err != nil {
		t.Skip("requires Just")
	}

	root := reporoot.Path(t)
	for _, recipe := range []string{"buildah-job-image-archive", "kaniko-job-image-archive", "runtime-image-archive"} {
		t.Run(recipe, func(t *testing.T) {
			lane := newPrepareLane(t)

			sentinel := filepath.Join(lane.dir, "archive.tar")
			if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}

			fake := filepath.Join(t.TempDir(), "go with spaces")
			writeExecutable(t, fake, "#!/usr/bin/env bash\nprintf '%s\\0' \"$@\" >\"$FAKE_GO_ARGV\"\nexit 2\n")

			args := []string{"--justfile", filepath.Join(root, "justfile"), recipe}
			if recipe == "runtime-image-archive" {
				args = append(args, "node")
			}

			args = append(args, lane.dir, "required", "2", "4096", "2")
			command := exec.CommandContext(t.Context(), just, args...)

			command.Env = append(slices.Clone(lane.env), "GO="+fake)

			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("unexpected admission: %s", output)
			}

			body, err := os.ReadFile(sentinel)
			if err != nil || string(body) != "keep" {
				t.Fatal("archive changed before admission")
			}

			argv, err := os.ReadFile(lane.goArgvFile)
			if err != nil || !strings.HasSuffix(string(argv), "image-build-settings\x00required\x002\x004096\x002\x00") {
				t.Fatalf("resource argv %q: %v\n%s", argv, err, output)
			}
		})
	}
}

func TestImagePreparationInheritedRequiredOnly(t *testing.T) {
	just, err := exec.LookPath("just")
	if err != nil {
		t.Skip("requires Just")
	}

	for _, route := range []string{"prepare-live", "test-live-image", "buildah-job-image-archive", "kaniko-job-image-archive", "runtime-image-archive"} {
		t.Run(route, func(t *testing.T) {
			lane := newPrepareLane(t)
			fake := filepath.Join(t.TempDir(), "go")
			writeExecutable(t, fake, "#!/usr/bin/env bash\nprintf '%s\\0' \"$@\" >\"$FAKE_GO_ARGV\"\nprintf '%s' \"${FORGE_CI_IMAGE_BUILD_BUDGET-unset}\" >\"$FAKE_JUST_ENV\"\n[[ ${FORGE_CI_IMAGE_BUILD_BUDGET-} != required ]] || exit 2\nexit 0\n")

			lab := filepath.Join(t.TempDir(), "lab with spaces")
			if err := os.Mkdir(lab, 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(lab, "justfile"), nil, 0o600); err != nil {
				t.Fatal(err)
			}

			sentinel := filepath.Join(lane.dir, "prepared.env")
			if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}

			env := append(slices.Clone(lane.env), "GO="+fake, "FORGE_CI_IMAGE_BUILD_BUDGET=required")
			if route == "prepare-live" {
				// No explicit resource words: only the inherited-requirement gate can refuse.
				_, code := runScript(t, lane.workDir, env, "prepare-live.sh", lab, lane.dir, "forgejo")
				if code != 2 {
					t.Fatalf("prepare-live exit %d", code)
				}
			} else {
				args := []string{"--justfile", filepath.Join(reporoot.Path(t), "justfile"), route}
				switch route {
				case "test-live-image":
					args = append(args, "signer", lab)
				case "runtime-image-archive":
					args = append(args, "node", lane.dir)
				default:
					args = append(args, lane.dir)
				}

				cmd := exec.CommandContext(t.Context(), just, args...)

				cmd.Env = env
				if out, err := cmd.CombinedOutput(); err == nil {
					t.Fatalf("inherited budget lost: %s", out)
				}
			}

			seen, err := os.ReadFile(lane.envFile)
			if err != nil || string(seen) != "required" {
				t.Fatalf("inherited requirement replaced: %q %v", seen, err)
			}

			argv, err := os.ReadFile(lane.goArgvFile)

			want := "image-build-settings\x00\x00\x00\x00\x00"
			if route == "prepare-live" {
				want = "image-build-settings-options\x00"
			}

			if err != nil || !strings.Contains(string(argv), want) {
				t.Fatalf("wrong gate: %q %v", argv, err)
			}

			body, err := os.ReadFile(sentinel)
			if err != nil || string(body) != "keep" {
				t.Fatal("destination changed")
			}

			if calls := readLines(t, lane.argvFile); len(calls) != 0 {
				t.Fatalf("publisher calls: %q", calls)
			}
		})
	}
}

func TestPrepareLive_RefusesAnArgumentItCannotName(t *testing.T) {
	t.Parallel()

	forgeLab := t.TempDir()
	if err := os.WriteFile(filepath.Join(forgeLab, "justfile"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	lane := newPrepareLane(t)
	for _, images := range [][]string{
		{"job-image=Go:localhost/nanolinter-go:dev"},
		{"job-image=go/x:localhost/nanolinter-go:dev"},
		{"job-image=go:"},
		{"job-image=go:--rm"},
		{"job-image=go:localhost/a b"},
		{"image=go:localhost/a"},
		{"job-image=go:localhost/a", "job-image=go:localhost/b"},
		{"gitlab=lane.cases"},
		{"gitlab="},
		{"Gitlab"},
		{"../gitlab"},
	} {
		stderr, status := runScript(t, lane.workDir, lane.env, "prepare-live.sh", append([]string{forgeLab, filepath.Join(t.TempDir(), "c"), "gitlab"}, images...)...)
		if status != 2 || !strings.Contains(stderr, "prepare-live: ") {
			t.Errorf("%q: status %d, want a refusal (2)\n%s", images, status, stderr)
		}
	}

	// A prepare is for the forges or lanes it names, so it names one.
	if stderr, status := runScript(t, lane.workDir, lane.env, "prepare-live.sh", forgeLab, filepath.Join(t.TempDir(), "c"), "job-image=go:localhost/a"); status != 2 ||
		!strings.Contains(stderr, "prepare-live: name what to prepare for") {
		t.Errorf("status %d, want the lanes asked for (2)\n%s", status, stderr)
	}

	if calls := readLines(t, lane.goArgvFile); calls != nil {
		t.Fatalf("a refused prepare asked the preflight: %q", calls)
	}

	if calls := readLines(t, lane.argvFile); calls != nil {
		t.Fatalf("a refused prepare ran %q", calls)
	}
}

func TestDemoCheckout_IsOneCommitOfTheDemo(t *testing.T) {
	t.Parallel()

	env := []string{
		"PATH=" + testenv.GNUPath(t, "bash", "ls", "cp", "mkdir", "dirname") + string(os.PathListSeparator) + testenv.ToolPath(t, "git"),
		"HOME=" + t.TempDir(),
		"LC_ALL=C",
	}
	checkout := filepath.Join(t.TempDir(), "hello-go-cli")

	if stderr, status := runScript(t, "", env, "demo-checkout.sh", "hello-go-cli", checkout); status != 0 {
		t.Fatalf("status %d\n%s", status, stderr)
	}

	git := func(args ...string) string {
		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", checkout}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}

		return strings.TrimSpace(string(out))
	}
	if count, dirty := git("rev-list", "--count", "HEAD"), git("status", "--porcelain", "--untracked-files=all"); count != "1" || dirty != "" {
		t.Fatalf("commits %s, status %q; want one commit and a clean tree", count, dirty)
	}

	if _, err := os.Stat(filepath.Join(checkout, "go.mod")); err != nil {
		t.Fatalf("the demo's files are not checked out: %v", err)
	}

	for _, args := range [][]string{{"hello-go-cli", checkout}, {"../demos", t.TempDir()}, {"hello-nothing", t.TempDir()}} {
		if stderr, status := runScript(t, "", env, "demo-checkout.sh", args...); status != 2 {
			t.Errorf("%q: status %d, want a refusal (2)\n%s", args, status, stderr)
		}
	}
}
