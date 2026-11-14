// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The Go module witness: a release of the layer is a signed tag, and to Go a
// tag is a published module version. This file carries no build tag, so the
// module path, the release fixture's one change and the consumer job are
// checked in every test run; the live owner is gomoduleconsumer_test.go.
const (
	// goModuleLibrary is the package a downstream module imports. The canary's
	// command stays as it is: the release still needs a binary to publish.
	goModuleLibrary = `// Package greeting is the library a downstream module imports.
package greeting

// Greet returns the greeting only this module's code produces.
func Greet(name string) string {
	return "hello, " + name + ", from the released go module"
}
`
	goModuleGreeting = "hello, fresh consumer, from the released go module"

	// The Go of the release fixture's build job, so the consumer needs no
	// toolchain the lab has not already pulled.
	goModuleConsumerImage = "docker.io/library/golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9"
)

// goModulePath is the path Go fetches a repository of the forge by, which is
// also the path its go.mod must declare. A forge reached on a port has none:
// a module path holds no colon, and Go asks https://<path>?go-get=1 on 443.
func goModulePath(host, owner, repo string) (string, error) {
	if strings.Contains(host, ":") {
		return "", fmt.Errorf("the forge is at %s, and a Go module path cannot name a port: %w", host, errs.ErrUnsupported)
	}

	path := host + "/" + owner + "/" + repo
	if err := module.CheckPath(path); err != nil {
		return "", fmt.Errorf("%w: %w", err, errs.ErrUnsupported)
	}

	return path, nil
}

// releaseWorkflowWithBinaryName names the canary's binary in the release
// fixture's build. The fixture leaves the name to the product, which takes the
// last element of the module path; a module named for its repository would
// change the name of every file the release is held to.
func releaseWorkflowWithBinaryName(rendered string) (string, error) {
	const anchor = "          main-package: ./cmd/layer-release-canary\n"

	if strings.Count(rendered, anchor) != 1 {
		return "", fmt.Errorf("the release fixture names the canary's main package %d times, want once: %w", strings.Count(rendered, anchor), errs.ErrValidation)
	}

	return strings.Replace(rendered, anchor, anchor+"          binary-name: layer-release-canary\n", 1), nil
}

// goModuleConsumerProbe is a downstream module's job: a new module that
// imports the library package, resolves the tag from the forge with no proxy,
// no checksum database and an empty module cache, and runs. The job holds no
// credential, so the repository must be readable by anyone.
//
// The verdict is in the job: the version Go resolved is the tag, at the commit
// the host found the signed tag pointing at, and the program printed the
// greeting only the library returns.
func goModuleConsumerProbe(trust, host, path, tag, commit string) string {
	commands := trust + `
set -eu
export GOPROXY=direct GOFLAGS=-mod=mod GOTOOLCHAIN=local
export GOPRIVATE='` + host + `' GONOSUMDB='` + host + `'
export GOMODCACHE="$PWD/modcache" GOCACHE="$PWD/gocache"
test ! -e "$GOMODCACHE"
mkdir consumer
cd consumer
go mod init example.invalid/hello-go-consumer
cat > main.go <<'SOURCE'
package main

import (
	"fmt"

	"` + path + `/greeting"
)

func main() { fmt.Println(greeting.Greet("fresh consumer")) }
SOURCE
go get '` + path + "@" + tag + `'
grep -Fq '` + path + " " + tag + `' go.mod
# Go records where a version came from: the ref it resolved and its commit.
go mod download -json '` + path + "@" + tag + `' > resolved.json
cat resolved.json
grep -Fq '"Version": "` + tag + `"' resolved.json
grep -Fq '"Ref": "refs/tags/` + tag + `"' resolved.json
grep -Fq '"Hash": "` + commit + `"' resolved.json
go run . > consumer-out.txt
cat consumer-out.txt
grep -Fxq '` + goModuleGreeting + `' consumer-out.txt
`

	return `on: [push]
jobs:
  consume:
    runs-on: ubuntu-latest
    container:
      image: ` + goModuleConsumerImage + `
    steps:
      - name: consume the released Go module by its tag
        run: |
          ` + indent(commands, 10) + "\n"
}

func TestGoModulePath(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		host, owner, repo, want string
	}{
		"a forge on 443":         {"forgejo.k3s.forgelab", "forgeci", "fci-layer-go-module-release", "forgejo.k3s.forgelab/forgeci/fci-layer-go-module-release"},
		"a forge on a port":      {"forgejo.compose.forgelab:8443", "forgeci", "fci-layer-go-module-release", ""},
		"a host without a dot":   {"forgejo", "forgeci", "fci-layer-go-module-release", ""},
		"an owner Go cannot say": {"forgejo.k3s.forgelab", "forge ci", "fci-layer-go-module-release", ""},
		"no repository":          {"forgejo.k3s.forgelab", "forgeci", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := goModulePath(tc.host, tc.owner, tc.repo)
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("goModulePath = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// The module witness changes one thing about the release it reuses, and the
// change must leave the build producing the files the release is held to.
func TestReleaseWorkflowWithBinaryName(t *testing.T) {
	t.Parallel()

	named, err := releaseWorkflowWithBinaryName(layerMinimalReleaseFixture)
	if err != nil {
		t.Fatal(err)
	}

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(named), &workflow); err != nil {
		t.Fatal(err)
	}

	builds := 0

	for _, step := range workflow.Jobs["build"].Steps {
		if !strings.Contains(step.Uses, "/.forgejo/actions/build-go@") {
			continue
		}

		builds++

		if step.With["binary-name"] != "layer-release-canary" || step.With["main-package"] != "./cmd/layer-release-canary" || step.With["artifact-name"] != "" {
			t.Fatalf("the build is called with %v, want the canary's binary named and nothing else renamed", step.With)
		}
	}

	if builds != 1 {
		t.Fatalf("the fixture calls build-go %d times, want once", builds)
	}

	if strings.Replace(named, "          binary-name: layer-release-canary\n", "", 1) != layerMinimalReleaseFixture {
		t.Fatal("naming the binary changed something else in the fixture")
	}

	// A fixture that already names it, or no longer builds that package, must
	// stop the witness instead of releasing under another name.
	for name, fixture := range map[string]string{
		"named twice":     named + named,
		"another package": strings.ReplaceAll(layerMinimalReleaseFixture, "./cmd/layer-release-canary", "./cmd/other"),
	} {
		if _, err := releaseWorkflowWithBinaryName(fixture); err == nil {
			t.Errorf("%s: the fixture was accepted", name)
		}
	}

	if !strings.Contains(layerMinimalReleaseFixture, "image: "+goModuleConsumerImage+"\n") {
		t.Error("the consumer's Go image is not the release build's")
	}
}

// goModuleProbeStubs stands in for the Go command. Every invocation checks
// that it runs as a downstream module would have to: straight from the forge,
// with a module cache of its own. STATE picks what the forge answered.
const goModuleProbeStubs = `origin="$PWD"
go() {
  test "$GOPROXY" = direct
  test "$GOFLAGS" = -mod=mod
  test "$GOPRIVATE" = fixture.invalid
  test "$GONOSUMDB" = fixture.invalid
  test "$GOMODCACHE" = "$origin/modcache"
  test "$PWD" = "$origin/consumer"
  case "$*" in
  'mod init example.invalid/hello-go-consumer')
    printf 'module example.invalid/hello-go-consumer\n' > go.mod
    ;;
  'get fixture.invalid/fixture/fci-lib@v0.0.1')
    grep -Fq '"fixture.invalid/fixture/fci-lib/greeting"' main.go
    [ "$STATE" = unrequired ] || printf 'require fixture.invalid/fixture/fci-lib v0.0.1\n' >> go.mod
    ;;
  'mod download -json fixture.invalid/fixture/fci-lib@v0.0.1')
    hash=0123abc ref=refs/tags/v0.0.1 version=v0.0.1
    [ "$STATE" != other-commit ] || hash=fedcba9
    [ "$STATE" != branch ] || ref=refs/heads/main
    [ "$STATE" != pseudo-version ] || version=v0.0.2-0.20260101000000-0123abc
    printf '{\n\t"Path": "fixture.invalid/fixture/fci-lib",\n\t"Version": "%s",\n\t"Origin": {\n\t\t"VCS": "git",\n\t\t"Hash": "%s",\n\t\t"Ref": "%s"\n\t}\n}\n' "$version" "$hash" "$ref"
    ;;
  'run .')
    if [ "$STATE" = other-output ]; then
      echo 'hello, fresh consumer, from elsewhere'
    else
      echo 'hello, fresh consumer, from the released go module'
    fi
    ;;
  *)
    return 1
    ;;
  esac
}
[ "$STATE" != stale-cache ] || mkdir modcache`

func TestGoModuleConsumerProbe_RenderedShell(t *testing.T) {
	t.Parallel()

	body := goModuleConsumerProbe(goModuleProbeStubs, "fixture.invalid", "fixture.invalid/fixture/fci-lib", "v0.0.1", "0123abc")

	job := decodeProbeFixture(t, body).Jobs["consume"]
	if len(job.Steps) != 1 || len(job.Steps[0].Env) != 0 {
		t.Fatalf("the consumer job must be one step holding no credential: %+v", job.Steps)
	}

	script := job.Steps[0].Run

	for _, shell := range []string{"sh", "bash"} {
		command := exec.CommandContext(t.Context(), shell, "-n")

		command.Stdin = strings.NewReader(script)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s syntax: %v: %s", shell, err, output)
		}
	}

	// Each state but the first is one way the witness must refuse: the module
	// cache is not fresh, Go did not require the tag, or what it resolved is
	// another commit, a branch, another version or another module's code.
	for _, state := range []string{"ready", "stale-cache", "unrequired", "other-commit", "branch", "pseudo-version", "other-output"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			err := runProbeShell(t, script, "STATE="+state)
			if (err == nil) != (state == "ready") {
				t.Fatalf("state %s: job exit %v", state, err)
			}
		})
	}
}
