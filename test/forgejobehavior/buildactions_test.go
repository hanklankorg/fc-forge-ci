// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// buildAction is what the two build actions share: one step, a prefix on its
// variables, and the verb it calls.
type buildAction struct {
	name, prefix, ecosystem string
	// optional are the inputs beyond the six both actions take, in the order
	// the step passes their flags.
	optional []string
}

func buildActions() []buildAction {
	return []buildAction{
		{name: "build-go", prefix: "BUILD_GO_", ecosystem: "go", optional: []string{"main-package", "build-tags", "ldflags"}},
		{name: "build-cargo", prefix: "BUILD_CARGO_", ecosystem: "cargo"},
	}
}

// variable is the step variable an input is bound to.
func (b buildAction) variable(input string) string {
	return b.prefix + strings.ToUpper(strings.ReplaceAll(input, "-", "_"))
}

// Each build action is one engine call: every input reaches its own flag
// (distinct values catch a swapped pair), the two switches are passed as
// values the engine parses, and a build without a version or a commit is
// refused before the engine starts. In a release the runner's ref and commit
// are the request's, so neither may fall back to them.
func TestBuildActionsForwardEveryInput(t *testing.T) {
	t.Parallel()

	for _, build := range buildActions() {
		t.Run(build.name, func(t *testing.T) {
			t.Parallel()
			action := loadAction(t, build.name)
			action.requireSteps(t, 1)

			inputs := append([]string{"version", "commit", "working-directory", "platforms", "artifact-name", "binary-name"}, build.optional...)
			inputs = append(inputs, "skip-tests", "build-sbom")

			bound := map[string]string{}
			env := make([]string, 0, len(inputs))

			for _, input := range inputs {
				actionInput := input
				if input == "build-sbom" {
					actionInput = "enable-build-sbom"
				}

				bound[build.variable(input)] = "${{ inputs." + actionInput + " }}"
				env = append(env, build.variable(input)+"="+input+"-v")
			}

			if build.ecosystem == "go" {
				bound[build.variable("source-date-epoch")] = "${{ inputs.source-date-epoch }}"
				env = append(env, build.variable("source-date-epoch")+"=")
			}

			action.requireStepEnv(t, 0, bound)

			for _, required := range []string{"version", "commit"} {
				if !action.Inputs[required].Required || action.Inputs[required].Default != nil {
					t.Errorf("%s input %s must be required and without a default", build.name, required)
				}
			}

			script := action.Runs.Steps[0].Run

			passed := make([]string, 0, 2*len(build.optional))
			for _, input := range build.optional {
				passed = append(passed, "--"+input, input+"-v")
			}

			want := slices.Concat([]string{
				"build", build.ecosystem, "run", "--version", "version-v", "--commit", "commit-v",
				"--working-dir", "working-directory-v", "--platforms", "platforms-v",
				"--artifact-name", "artifact-name-v", "--binary-name", "binary-name-v",
			}, passed, []string{"--skip-tests=skip-tests-v", "--build-sbom=build-sbom-v"})

			box := newSandbox(t, build.name)
			if got := box.run(script, env...); got.status != 0 || !slices.Equal(box.argv(), want) {
				t.Fatalf("status %d argv %q, want %q; stderr %s", got.status, box.argv(), want, got.stderr)
			}

			for _, required := range []string{"version", "commit"} {
				refused := newSandbox(t, build.name)
				cleared := slices.Clone(env)
				cleared[slices.Index(cleared, build.variable(required)+"="+required+"-v")] = build.variable(required) + "="

				got := refused.run(script, cleared...)
				if got.status == 0 || refused.argv() != nil || !strings.Contains(got.stderr, required+" is required") {
					t.Fatalf("an empty %s: status %d argv %q stderr %q", required, got.status, refused.argv(), got.stderr)
				}
			}
		})
	}
}

// The Go build's date stamp comes from SOURCE_DATE_EPOCH. The input sets it,
// and an empty input leaves the job's own value alone instead of clearing it.
func TestBuildGoSourceDateEpoch(t *testing.T) {
	t.Parallel()

	build := buildActions()[0]
	script := actionStep(t, build.name, "Build Go module")

	for _, test := range []struct {
		name, input, job, want string
	}{
		{"the input sets it", "1700000000", "", "1700000000"},
		{"the input wins over the job's value", "1700000000", "1600000000", "1700000000"},
		{"an empty input keeps the job's value", "", "1600000000", "1600000000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, build.name)

			env := append(installRecordingFakes(box),
				build.variable("version")+"=v1.2.3", build.variable("commit")+"=abc", build.variable("working-directory")+"=.",
				build.variable("platforms")+"=linux/amd64", build.variable("artifact-name")+"=", build.variable("binary-name")+"=",
				build.variable("main-package")+"=.", build.variable("build-tags")+"=", build.variable("ldflags")+"=",
				build.variable("skip-tests")+"=false", build.variable("build-sbom")+"=true",
				build.variable("source-date-epoch")+"="+test.input)
			if test.job != "" {
				env = append(env, "SOURCE_DATE_EPOCH="+test.job)
			}

			if got := box.run(script, env...); got.status != 0 {
				t.Fatalf("status %d stderr %s", got.status, got.stderr)
			}

			if got := recorded(box).env["SOURCE_DATE_EPOCH"]; got != test.want {
				t.Fatalf("SOURCE_DATE_EPOCH = %q, want %q", got, test.want)
			}
		})
	}
}

// fakeGo stands in for the Go toolchain: it logs each command line with the
// target platform, and writes the file a build names.
const fakeGo = `#!/usr/bin/env bash
set -euo pipefail
printf '%s/%s %s\n' "${GOOS:-}" "${GOARCH:-}" "$*" >>"${FAKE_TOOL_LOG:?}"
if [ "$1" = build ]; then
  while [ "$#" -gt 1 ]; do
    if [ "$1" = -o ]; then mkdir -p "$(dirname "$2")"; printf 'binary for %s/%s\n' "${GOOS}" "${GOARCH}" >"$2"; fi
    shift
  done
fi
`

// fakeCycloneDXGoMod writes the SBOM the engine asks for.
const fakeCycloneDXGoMod = `#!/usr/bin/env bash
set -euo pipefail
printf 'cyclonedx-gomod %s\n' "$*" >>"${FAKE_TOOL_LOG:?}"
while [ "$#" -gt 1 ]; do
  if [ "$1" = -output ]; then printf '{"bomFormat":"CycloneDX"}\n' >"$2"; fi
  shift
done
`

// fakeCargo stands in for cargo: metadata for one binary crate, and a build
// that writes the executable where cargo would.
const fakeCargo = `#!/usr/bin/env bash
set -euo pipefail
printf 'cargo %s\n' "$*" >>"${FAKE_TOOL_LOG:?}"
case "$1" in
  metadata)
    printf '{"packages":[{"name":"app","version":"0.1.0","manifest_path":"%s/Cargo.toml","targets":[{"name":"app","kind":["bin"]}]}],"workspace_root":"%s"}\n' "$PWD" "$PWD"
    ;;
  cyclonedx) printf '{"bomFormat":"CycloneDX"}\n' >bom.json ;;
  build)
    triple=''
    while [ "$#" -gt 1 ]; do
      if [ "$1" = --target ]; then triple=$2; fi
      shift
    done
    mkdir -p "target/${triple}/release"
    printf 'binary for %s, version %s at %s\n' "${triple}" "${FORGE_CI_VERSION}" "${FORGE_CI_COMMIT}" >"target/${triple}/release/app"
    chmod +x "target/${triple}/release/app"
    ;;
esac
`

// goBuildInputs are the Go build step's variables for a two-platform release
// build named app, on a runner whose own ref and commit are the request's.
func goBuildInputs(commit string) []string {
	build := buildActions()[0]

	return []string{
		"FORGEJO_REF_NAME=release-request/v1.2.3", "FORGEJO_SHA=" + strings.Repeat("f", 40),
		build.variable("version") + "=v1.2.3", build.variable("commit") + "=" + commit, build.variable("working-directory") + "=.",
		build.variable("platforms") + "=linux/amd64,linux/arm64", build.variable("artifact-name") + "=app", build.variable("binary-name") + "=",
		build.variable("main-package") + "=.", build.variable("build-tags") + "=", build.variable("ldflags") + "=",
		build.variable("skip-tests") + "=false", build.variable("build-sbom") + "=true",
		build.variable("source-date-epoch") + "=1700000000",
	}
}

// goBuildSandbox is a Go module with a stand-in toolchain on PATH.
func goBuildSandbox(t *testing.T) (*sandbox, []string) {
	t.Helper()

	box, env := engineSandbox(t, "build-go", map[string]string{"go": fakeGo, "cyclonedx-gomod": fakeCycloneDXGoMod})
	box.write("go.mod", "module example.invalid/tools/app\n\ngo 1.27\n", 0o600)

	return box, env
}

// The Go build step against the CLI of this tree, with a stand-in toolchain:
// the modules, the tests, the Build SBOM, and one binary per platform stamped
// with the release's version and commit and not the runner's.
func TestBuildGoThroughTheEngine(t *testing.T) {
	t.Parallel()

	const commit = "0123456789abcdef0123456789abcdef01234567"

	box, env := goBuildSandbox(t)

	got := box.run(actionStep(t, "build-go", "Build Go module"), append(env, goBuildInputs(commit)...)...)
	if got.status != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
	}

	log := readSandboxFile(t, box, "tool-log")
	amd64 := filepath.Join(box.root, "dist", "linux-amd64", "app-linux-amd64")
	arm64 := filepath.Join(box.root, "dist", "linux-arm64", "app-linux-arm64")

	for _, want := range []string{
		"/ mod download", "/ test ./...", "cyclonedx-gomod mod -json -output .forge-ci/go-build-sbom/app/bom.json .",
		"linux/amd64 build -trimpath -buildvcs=false -ldflags -s -w -X main.version=1.2.3 -X main.commit=" + commit + " -X main.date=2023-11-14T22:13:20Z -o " + amd64 + " .",
		"linux/arm64 build ", "-o " + arm64 + " .",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("the toolchain was not asked for %q:\n%s", want, log)
		}
	}

	for _, written := range []string{"dist/linux-amd64/app-linux-amd64", "dist/linux-arm64/app-linux-arm64", ".forge-ci/go-build-sbom/app/bom.json"} {
		if readSandboxFile(t, box, written) == "" {
			t.Errorf("%s is empty", written)
		}
	}
}

// The Cargo build step against the CLI of this tree: the locked tests, the
// Build SBOM, and one binary per platform under the name and layout the Go
// build uses, so the same declaration hands either off.
func TestBuildCargoThroughTheEngine(t *testing.T) {
	t.Parallel()

	build := buildActions()[1]
	box, env := engineSandbox(t, build.name, map[string]string{"cargo": fakeCargo})
	box.write("Cargo.toml", "[package]\nname = \"app\"\nversion = \"0.1.0\"\n", 0o600)

	got := box.run(actionStep(t, build.name, "Build Cargo project"), append(env,
		"FORGEJO_REF_NAME=release-request/v1.2.3", "FORGEJO_SHA="+strings.Repeat("f", 40),
		build.variable("version")+"=v1.2.3", build.variable("commit")+"=abc123", build.variable("working-directory")+"=.",
		build.variable("platforms")+"=linux/amd64,linux/arm64", build.variable("artifact-name")+"=", build.variable("binary-name")+"=",
		build.variable("skip-tests")+"=false", build.variable("build-sbom")+"=true")...)
	if got.status != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", got.status, got.stdout, got.stderr)
	}

	log := readSandboxFile(t, box, "tool-log")
	for _, want := range []string{"cargo test --locked", "cargo cyclonedx ", "--target x86_64-unknown-linux-", "--target aarch64-unknown-linux-"} {
		if !strings.Contains(log, want) {
			t.Fatalf("cargo was not asked for %q:\n%s", want, log)
		}
	}

	for _, binary := range []string{"dist/linux-amd64/app-linux-amd64", "dist/linux-arm64/app-linux-arm64"} {
		if content := readSandboxFile(t, box, binary); !strings.Contains(content, "version 1.2.3 at abc123") {
			t.Errorf("%s = %q, want the release's version and commit", binary, content)
		}
	}
}
