// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestImagePreparationShellCheckSourceContract(t *testing.T) {
	shellcheck, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Fatal("requires installed ShellCheck")
	}

	root := t.TempDir()
	for _, name := range []string{".shellcheckrc", "scripts/ci/prepare-live.sh", "scripts/ci/publish-live-image.sh", "scripts/ci/image-build-settings.sh"} {
		copyImageFixture(t, root, name)
	}
	// Match the maintained adapter's file/config inputs, including the helper as
	// a directly linted .sh file. Its output-array contract must stand on its own.
	cmd := exec.CommandContext(t.Context(), shellcheck, "--format=json", "--rcfile=.shellcheckrc", "scripts/ci/prepare-live.sh", "scripts/ci/publish-live-image.sh", "scripts/ci/image-build-settings.sh")

	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("maintained ShellCheck inputs: %v: %s", err, output)
	}

	helper := filepath.Join(root, "scripts/ci/image-build-settings.sh")

	body, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	// A deliberate fixture-only diagnostic proves the publisher's static source
	// declaration resolves the real helper, without relying on the rc's SC1091.
	if err := os.WriteFile(helper, append(body, []byte("\nprintf '%s\\n' $contract_sentinel\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd = exec.CommandContext(t.Context(), shellcheck, "--norc", "--external-sources", "--check-sourced", "--format=json", "scripts/ci/publish-live-image.sh")
	cmd.Dir = root
	output, err := cmd.Output()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("sourced diagnostic: %v: %s", err, output)
	}

	var findings []struct {
		File string `json:"file"`
		Code int    `json:"code"`
	}
	if err := json.Unmarshal(output, &findings); err != nil {
		t.Fatal(err)
	}

	for _, finding := range findings {
		if finding.Code == 2086 && finding.File == "scripts/ci/image-build-settings.sh" {
			return
		}
	}

	t.Fatalf("publisher did not resolve helper diagnostic: %s", output)
}

const fakeImageSettingsGo = `#!/usr/bin/env bash
case "$*" in
*image-build-settings*) printf '%s\n' required 3 2048 1 --cpu-period 100000 --cpu-quota 300000 --memory 2147483648 --memory-swap 2147483648 --jobs 1 --retry 0 ;;
*runner-arch*) echo amd64 ;;
*consumer-trial-layer*) echo https://fixture.invalid/source ;;
*) exit 89 ;;
esac
`

func TestImagePreparationSettingsOutputArray(t *testing.T) {
	for _, required := range []bool{false, true} {
		for _, output := range []bool{false, true} {
			root := t.TempDir()
			fake := filepath.Join(root, "go")
			writeExecutable(t, fake, "#!/usr/bin/env bash\nprintf '%s' \"$SETTINGS\"\n")

			settings, want := "default\n\n\n\n", "default\n"
			if required {
				settings = "required\n3\n2048\n1\n--cpu-period\n100000\n--cpu-quota\n300000\n--memory\n2147483648\n--memory-swap\n2147483648\n--jobs\n1\n--retry\n0\n"
				want = "required\n"
			}

			script := `source "$HELPER"
if [[ $OUTPUT == yes ]]; then
  declare -a actual=(stale)
  image_build_settings actual image-build-settings '' '' '' '' || exit
  printf '%s\n' "$FORGE_CI_IMAGE_BUILD_BUDGET" "${#actual[@]}"
  if ((${#actual[@]})); then printf '%s\0' "${actual[@]}"; fi
else
  image_build_settings '' image-build-settings-options || exit
  for variable in actual image_build_args native_args; do
    ! declare -p "$variable" >/dev/null 2>&1 || exit 99
  done
  printf '%s\n' "$FORGE_CI_IMAGE_BUILD_BUDGET"
fi
`

			outputMode := "no"
			if output {
				outputMode = "yes"

				want += "0\n"
				if required {
					want = "required\n12\n--cpu-period\x00100000\x00--cpu-quota\x00300000\x00--memory\x002147483648\x00--memory-swap\x002147483648\x00--jobs\x001\x00--retry\x000\x00"
				}
			}

			cmd := exec.CommandContext(t.Context(), testBash(t), "-euo", "pipefail", "-c", script)
			cmd.Env = append(os.Environ(), "GO="+fake, "HELPER="+filepath.Join(reporoot.Path(t), "scripts/ci/image-build-settings.sh"), "SETTINGS="+settings, "OUTPUT="+outputMode)

			stdout, err := cmd.Output()
			if err != nil || string(stdout) != want {
				t.Fatalf("required=%v output=%v: %v: got %q, want %q", required, output, err, stdout, want)
			}
		}
	}
}

func TestImagePreparationOwnersApplyNativeLimits(t *testing.T) {
	just, err := exec.LookPath("just")
	if err != nil {
		t.Skip("requires Just")
	}

	for _, owner := range []string{"runtime", "buildah", "signer", "kaniko-job", "buildah-job"} {
		for _, status := range []string{"17", "143"} {
			t.Run(owner+"/"+status, func(t *testing.T) {
				root, bin := t.TempDir(), t.TempDir()
				for _, name := range []string{"justfile", "scripts/ci/image-build-settings.sh", "scripts/ci/publish-live-image.sh"} {
					copyImageFixture(t, root, name)
				}

				writeExecutable(t, filepath.Join(root, "scripts/ci/engine-binary.sh"), "#!/usr/bin/env bash\nprintf '%s\\0' \"$GOMAXPROCS\" \"$GOMEMLIMIT\" \"$GOFLAGS\" >\"$RECEIPT.compiler\"\n")

				for _, directory := range []string{"containers/runtime", "scripts/bootstrap", "templates"} {
					if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
						t.Fatal(err)
					}
				}

				writeExecutable(t, filepath.Join(bin, "go"), fakeImageSettingsGo)
				writeExecutable(t, filepath.Join(bin, "jq"), "#!/usr/bin/env bash\nexit 0\n")
				writeExecutable(t, filepath.Join(bin, "git"), "#!/usr/bin/env bash\ncase \"$*\" in *status*) exit 0 ;; *) echo fixture ;; esac\n")
				writeExecutable(t, filepath.Join(bin, "podman"), `#!/usr/bin/env bash
case "$*" in
info*) echo amd64 ;;
"image exists"*) exit 1 ;;
build*) printf '%s\0' "$@" >>"$RECEIPT.argv"; if [[ $BUILD_STATUS == 143 ]]; then kill -TERM "$$"; fi; exit "$BUILD_STATUS" ;;
*) printf '%s\n' "$*" >>"$RECEIPT.forbidden"; exit 88 ;;
esac
`)
				archive := filepath.Join(root, "not-created")
				cmd := imageOwnerCommand(t, just, root, owner, archive)
				cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GO="+filepath.Join(bin, "go"), "RECEIPT="+filepath.Join(root, "receipt"), "BUILD_STATUS="+status, "FORGE_CI_IMAGE_BUILD_BUDGET=required", "FORGE_CI_IMAGE_BUILD_CPUS=3", "FORGE_CI_IMAGE_BUILD_MEMORY_MIB=2048", "FORGE_CI_IMAGE_BUILD_COMPILER_JOBS=1")
				output, err := cmd.CombinedOutput()

				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != map[string]int{"17": 17, "143": 143}[status] {
					t.Fatalf("exit: %v: %s", err, output)
				}

				argv, err := os.ReadFile(filepath.Join(root, "receipt.argv"))
				if err != nil {
					t.Fatal(err)
				}

				words := strings.Split(string(argv), "\x00")

				for _, pair := range [][]string{{"--cpu-period", "100000"}, {"--cpu-quota", "300000"}, {"--memory", "2147483648"}, {"--memory-swap", "2147483648"}, {"--jobs", "1"}, {"--retry", "0"}} {
					found := false
					for i := 0; i+1 < len(words); i++ {
						found = found || slices.Equal(words[i:i+2], pair)
					}

					if !found {
						t.Fatalf("missing %q in %q", pair, words)
					}
				}

				if strings.Count(string(argv), "build\x00") != 1 {
					t.Fatalf("retried build: %q", argv)
				}

				if _, err := os.Stat(archive); !os.IsNotExist(err) {
					t.Fatal("failed build staged destination")
				}

				if _, err := os.Stat(filepath.Join(root, "receipt.forbidden")); !os.IsNotExist(err) {
					t.Fatal("save/publication after failure")
				}

				if owner == "runtime" {
					compiler, err := os.ReadFile(filepath.Join(root, "receipt.compiler"))
					if err != nil || !strings.HasPrefix(string(compiler), "1\x002048MiB\x00") || !strings.Contains(string(compiler), "-p=1") {
						t.Fatalf("compiler: %q %v", compiler, err)
					}
				}
			})
		}
	}
}

// imageOwnerCommand runs one image preparation owner of the tree copied to
// root: the justfile's archive recipes, or publish-live-image.sh for a kind.
func imageOwnerCommand(t *testing.T, just, root, owner, archive string) *exec.Cmd {
	t.Helper()

	var cmd *exec.Cmd

	switch owner {
	case "runtime":
		cmd = exec.CommandContext(t.Context(), just, "--justfile", filepath.Join(root, "justfile"), "runtime-image-archive", "go", archive, "required", "3", "2048", "1")
	case "buildah":
		cmd = exec.CommandContext(t.Context(), just, "--justfile", filepath.Join(root, "justfile"), "buildah-job-image-archive", archive, "required", "3", "2048", "1")
	default:
		lab := t.TempDir()
		if err := os.WriteFile(filepath.Join(lab, "justfile"), nil, 0o600); err != nil {
			t.Fatal(err)
		}

		cmd = exec.CommandContext(t.Context(), testBash(t), filepath.Join(root, "scripts/ci/publish-live-image.sh"), owner, lab, archive)
	}

	cmd.Dir = root

	return cmd
}

// Every image a live preparation builds on this host is tagged with the
// commit it is built from, so a build for another commit never takes the
// local name a running lane's GitLab job finds its image under; inputs with
// uncommitted changes are refused before anything is built.
func TestImagePreparationOwnersTagTheImageByCommit(t *testing.T) {
	just, err := exec.LookPath("just")
	if err != nil {
		t.Skip("requires Just")
	}

	const commit = "0123456789abcdef0123456789abcdef01234567"

	repositories := map[string]string{
		"runtime":     "localhost/forge-ci-runtime-go",
		"buildah":     "localhost/forge-ci-buildah-job",
		"signer":      "localhost/forge-ci-live-signer",
		"kaniko-job":  "localhost/forge-ci-live-kaniko-job",
		"buildah-job": "localhost/forge-ci-live-buildah-job",
	}
	for owner, repository := range repositories {
		for _, tree := range []string{"clean", "dirty"} {
			t.Run(owner+"/"+tree, func(t *testing.T) {
				root, bin := t.TempDir(), t.TempDir()
				for _, name := range []string{"justfile", "scripts/ci/image-build-settings.sh", "scripts/ci/publish-live-image.sh"} {
					copyImageFixture(t, root, name)
				}

				for _, directory := range []string{"containers/runtime", "scripts/bootstrap", "templates"} {
					if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
						t.Fatal(err)
					}
				}

				writeExecutable(t, filepath.Join(root, "scripts/ci/engine-binary.sh"), "#!/usr/bin/env bash\nexit 0\n")
				writeExecutable(t, filepath.Join(bin, "go"), fakeImageSettingsGo)
				writeExecutable(t, filepath.Join(bin, "jq"), "#!/usr/bin/env bash\nexit 0\n")
				writeExecutable(t, filepath.Join(bin, "just"), "#!/usr/bin/env bash\necho lab.invalid/forgeci/image@sha256:1\n")
				writeExecutable(t, filepath.Join(bin, "git"), `#!/usr/bin/env bash
case "$*" in
*status*) [[ $TREE == clean ]] || echo " M containers/changed" ;;
*rev-parse*) echo "$COMMIT" ;;
*) echo fixture ;;
esac
`)
				writeExecutable(t, filepath.Join(bin, "podman"), `#!/usr/bin/env bash
case "$*" in
info*) echo amd64 ;;
"image exists"*) exit 1 ;;
build*) printf '%s\0' "$@" >"$RECEIPT.build" ;;
save*) printf '%s' "${@: -1}" >"$RECEIPT.save" && : >"${@: -2:1}" ;;
*) exit 88 ;;
esac
`)

				cmd := imageOwnerCommand(t, just, root, owner, filepath.Join(root, "archive"))
				cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GO="+filepath.Join(bin, "go"), "RECEIPT="+filepath.Join(root, "receipt"), "TREE="+tree, "COMMIT="+commit, "FORGE_CI_IMAGE_BUILD_BUDGET=required", "FORGE_CI_IMAGE_BUILD_CPUS=3", "FORGE_CI_IMAGE_BUILD_MEMORY_MIB=2048", "FORGE_CI_IMAGE_BUILD_COMPILER_JOBS=1")
				output, err := cmd.CombinedOutput()
				build, buildErr := os.ReadFile(filepath.Join(root, "receipt.build"))

				if tree == "dirty" {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(output), "uncommitted changes") || buildErr == nil {
						t.Fatalf("dirty inputs admitted: %v: %s", err, output)
					}

					return
				}

				if err != nil || buildErr != nil {
					t.Fatalf("build: %v %v: %s", err, buildErr, output)
				}

				image := repository + ":" + commit
				if !strings.Contains(string(build), "\x00--tag\x00"+image+"\x00") {
					t.Fatalf("built %q, want the tag %s", build, image)
				}

				if saved, err := os.ReadFile(filepath.Join(root, "receipt.save")); err != nil || string(saved) != image {
					t.Fatalf("saved %q, want %s: %v", saved, image, err)
				}
			})
		}
	}
}

func copyImageFixture(t *testing.T, root, name string) {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), name))
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestImagePreparationPrepareLivePassesSettingsBeforeCompilation(t *testing.T) {
	root, bin := t.TempDir(), t.TempDir()
	for _, name := range []string{"scripts/ci/prepare-live.sh", "scripts/ci/image-build-settings.sh"} {
		copyImageFixture(t, root, name)
	}

	writeExecutable(t, filepath.Join(bin, "go"), fakeImageSettingsGo)
	writeExecutable(t, filepath.Join(bin, "git"), "#!/usr/bin/env bash\ncase \"$*\" in *status*) exit 0 ;; *) echo fixture ;; esac\n")
	writeExecutable(t, filepath.Join(root, "scripts/ci/engine-binary.sh"), `#!/usr/bin/env bash
printf '%s\0' "$@" >"$RECEIPT.argv"
printf '%s\0' "$FORGE_CI_IMAGE_BUILD_BUDGET" "$FORGE_CI_IMAGE_BUILD_CPUS" "$FORGE_CI_IMAGE_BUILD_MEMORY_MIB" "$FORGE_CI_IMAGE_BUILD_COMPILER_JOBS" "$GOMAXPROCS" "$GOMEMLIMIT" "$GOFLAGS" >"$RECEIPT.env"
exit 17
`)

	lab, destination := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(lab, "justfile"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	sentinel := filepath.Join(destination, "prepared.env")
	if err := os.WriteFile(sentinel, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), testBash(t), filepath.Join(root, "scripts/ci/prepare-live.sh"), lab, destination, "forgejo", "build-budget=required", "build-cpus=3", "build-memory-mib=2048", "compiler-jobs=1")
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GO="+filepath.Join(bin, "go"), "RECEIPT="+filepath.Join(root, "receipt"))
	output, err := cmd.CombinedOutput()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 {
		t.Fatalf("%v: %s", err, output)
	}

	settings, err := os.ReadFile(filepath.Join(root, "receipt.env"))
	if err != nil || !strings.HasPrefix(string(settings), "required\x003\x002048\x001\x001\x002048MiB\x00") || !strings.Contains(string(settings), "-p=1") {
		t.Fatalf("settings %q: %v", settings, err)
	}

	argv, err := os.ReadFile(filepath.Join(root, "receipt.argv"))
	if err != nil || string(argv) != "check\x00" {
		t.Fatalf("first effect %q: %v", argv, err)
	}

	body, err := os.ReadFile(sentinel)
	if err != nil || string(body) != "preserve\n" {
		t.Fatal("compiler failure changed the prepared destination")
	}
}
