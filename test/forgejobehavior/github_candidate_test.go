// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestGoJobInstallsOnlyPinnedJustBeforeRunningItsRecipes(t *testing.T) {
	t.Parallel()

	file := ".github/workflows/self-pullrequest.yml"

	var workflow struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	loadYAML(t, file, &workflow)

	installed, recipes := false, 0

	for _, step := range workflow.Jobs["go"].Steps {
		if step.Name == "Install pinned Just" {
			installed = true
		}

		if step.Run == "just test-smoke" || step.Run == "just test-fuzz" {
			recipes++

			if !installed {
				t.Fatal("Go test recipes require Just to be installed first")
			}
		}
	}

	if recipes != 2 {
		t.Fatal("Go CI must retain both smoke and fuzz-seed recipes")
	}

	setup := jobStep(t, file, "go", "Install pinned Just")
	box := newSandbox(t, "just-setup")

	for _, name := range []string{"install-mise.sh", "install-common.sh"} {
		path := "scripts/bootstrap/" + name
		box.write(path, string(reporoot.ReadFile(t, path)), 0o600)
	}

	// The runner's mise is reused only at the installer's pin.
	_, pin, _ := strings.Cut(string(reporoot.ReadFile(t, "scripts/bootstrap/install-mise.sh")), `readonly MISE_VERSION="`)
	pin, _, _ = strings.Cut(pin, `"`)

	box.write("bin/mise", `#!/bin/sh
set -eu
case "$*" in
  --version) printf '%s linux-x64 (fixture)\n' "$MISE_PIN" ;;
  'install aqua:casey/just') printf installed >"$INSTALL_MARKER" ;;
  'bin-paths aqua:casey/just') test -f "$INSTALL_MARKER"; printf '%s\n' "$PINNED_JUST_BIN" ;;
  *) printf 'unexpected tool request: %s\n' "$*" >&2; exit 98 ;;
esac
`, 0o700)
	path := filepath.Join(box.root, "github-path")
	just := filepath.Join(box.root, "pinned-just-bin")

	result := box.run(setup.Run, "PATH="+filepath.Join(box.root, "bin")+":/usr/bin:/bin",
		"GITHUB_PATH="+path, "PINNED_JUST_BIN="+just, "MISE_PIN="+pin, "INSTALL_MARKER="+filepath.Join(box.root, "installed"))
	if result.status != 0 {
		t.Fatalf("Just-only bootstrap failed: %s", result.stderr)
	}

	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != just+"\n" {
		t.Fatalf("pinned Just binary directory was not exposed to later steps: %q: %v", contents, err)
	}
}

func TestBlackboxSuiteUsesFreshBinaryFromTheExactPRCheckout(t *testing.T) {
	t.Parallel()

	file := ".github/workflows/self-pullrequest.yml"
	build := jobStep(t, file, "integration-testsuite", "Build exact PR candidate")

	test := jobStep(t, file, "integration-testsuite", "Run the black-box suite against this checkout")
	if build.Env["EXPECTED_SOURCE_SHA"] != "${{ github.sha }}" ||
		test.Env["FORGE_CI_TEST_BINARY"] != "${{ runner.temp }}/forge-ci-candidate" {
		t.Fatal("suite must use the fresh binary built from the PR's exact source SHA")
	}

	for _, matching := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching source", false: "wrong source"}[matching], func(t *testing.T) {
			t.Parallel()
			box := newSandbox(t, "candidate-build")
			box.write("bin/git", "#!/bin/sh\nset -eu\ntest \"$*\" = 'rev-parse HEAD'\nprintf '%s\\n' \"$ACTUAL_SOURCE_SHA\"\n", 0o700)
			box.write("bin/go", fakeEngine, 0o700)

			sha := strings.Repeat("a", 40)

			actual := sha
			if !matching {
				actual = strings.Repeat("b", 40)
			}

			result := box.run(build.Run,
				"PATH="+filepath.Join(box.root, "bin")+":"+os.Getenv("PATH"),
				"RUNNER_TEMP="+box.root, "EXPECTED_SOURCE_SHA="+sha, "ACTUAL_SOURCE_SHA="+actual)
			if !matching {
				if result.status == 0 || len(box.argv()) != 0 {
					t.Fatal("source mismatch must refuse before compiling any candidate")
				}

				return
			}

			want := []string{"build", "-trimpath", "-buildvcs=false", "-ldflags", "-X main.commit=" + sha,
				"-o", filepath.Join(box.root, "forge-ci-candidate"), "./cmd/forge-ci"}
			if result.status != 0 || !slices.Equal(box.argv(), want) {
				t.Fatalf("fresh candidate build: %v: %s", box.argv(), result.stderr)
			}
		})
	}
}
