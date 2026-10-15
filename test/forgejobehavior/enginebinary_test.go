// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const (
	// binaryScript is the one definition of how the layer's binary is built.
	binaryScript = "scripts/ci/forgejo-binary.sh"
	// trackedChecksum names the binary the layer runs: the tree tracks this
	// one line and a job downloads the binary by it.
	trackedChecksum = ".forgejo/bin/forge-ci.sha256"
)

// builtEngine is the CLI of this tree, built once per test binary by
// binaryScript into a directory TestMain removes.
type builtEngine struct {
	once sync.Once
	dir  string
	path string
	err  error
}

//nolint:gochecknoglobals // one build shared by every test of the package; TestMain removes it.
var engine builtEngine

func TestMain(m *testing.M) {
	code := evidence.Main(m)

	if engine.dir != "" {
		_ = os.RemoveAll(engine.dir)
	}

	os.Exit(code)
}

// script runs binaryScript and returns its standard output. The script gets
// the variables it reads and no others, so what it builds does not depend on
// the shell the tests were started from.
func script(ctx context.Context, root string, arguments ...string) (string, error) {
	var stdout, stderr bytes.Buffer

	command := exec.CommandContext(ctx, "bash", append([]string{filepath.Join(root, binaryScript)}, arguments...)...) //nolint:gosec // G204: this repository's own script with test-authored arguments.
	command.Dir = root
	command.Stdout, command.Stderr = &stdout, &stderr

	command.Env = []string{}

	for _, name := range []string{"PATH", "HOME", "GO", "GOPATH", "GOMODCACHE", "GOCACHE"} {
		if value, set := os.LookupEnv(name); set {
			command.Env = append(command.Env, name+"="+value)
		}
	}

	if err := command.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", binaryScript, strings.Join(arguments, " "), err, stderr.String())
	}

	return stdout.String(), nil
}

// git returns the trimmed output of a Git command in the repository.
func git(t *testing.T, root string, arguments ...string) (string, error) {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, arguments...)...) //nolint:gosec // G204: git with test-authored arguments.
	out, err := command.Output()

	return strings.TrimSpace(string(out)), err
}

// forgeCIEngine returns the engine the tests of this package run: the CLI
// built from this tree, so a change to a verb and to the workflow shell that
// calls it are tested together. TestRecordedEngineChecksumIsABuildOfThisTree
// holds the checksum a job downloads the binary by to the same build.
func forgeCIEngine(t *testing.T) string {
	t.Helper()

	root := reporoot.Path(t)

	engine.once.Do(func() {
		// Not t.TempDir: the build outlives the test that first asks for it.
		engine.dir, engine.err = os.MkdirTemp("", "forgejobehavior-engine-") //nolint:usetesting // shared by the package; TestMain removes it.
		if engine.err != nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		engine.path = filepath.Join(engine.dir, "forge-ci")
		_, engine.err = script(ctx, root, "build", engine.path)
	})

	if engine.err != nil {
		t.Fatalf("build the engine from this tree: %v", engine.err)
	}

	return engine.path
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// recordedChecksum returns the digest in the tracked checksum file, which is
// one line naming bin/forge-ci, the path relative to the layer root.
func recordedChecksum(t *testing.T) string {
	t.Helper()

	digest, name, found := strings.Cut(strings.TrimSuffix(string(reporoot.ReadFile(t, trackedChecksum)), "\n"), "  ")
	if !found || name != "bin/forge-ci" || len(digest) != sha256.Size*2 || strings.ContainsAny(digest, "\n ") {
		t.Fatalf("%s is not one `<sha256>  bin/forge-ci` line; rewrite it with `just refresh-forgejo-binary`", trackedChecksum)
	}

	return digest
}

// The checksum the tree records is the build of the tree it sits in, so the
// binary a job downloads by it cannot be other code than the sources beside
// it. The build names no commit: it follows the working tree as it is, and a
// commit that changes no build input leaves it valid. A change to Go source,
// go.mod or go.sum fails here until the checksum is recorded again, in the
// same commit or in one after it; what must hold is the tip.
//
// It is not proven, and says so, when the local Go toolchain is not the
// release go.mod pins: another toolchain writes other bytes. The strict
// profile turns that into a failure.
func TestRecordedEngineChecksumIsABuildOfThisTree(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	recorded := recordedChecksum(t)

	if pinned, local := pinnedToolchain(t, root), localToolchain(t); local != pinned {
		evidence.NotProvenf(t, "the local Go toolchain is not the release go.mod pins, so it cannot reproduce the layer binary")
	}

	if built := fileSHA256(t, forgeCIEngine(t)); built != recorded {
		t.Fatalf("%s does not record the build of this tree: recorded %s, built %s.\n"+
			"A build input changed since it was recorded. Run `just refresh-forgejo-binary`, or write this line to the file, and commit it:\n%s  bin/forge-ci",
			trackedChecksum, recorded, built, built)
	}
}

// pinnedToolchain returns the Go release go.mod names, as `go env GOVERSION`
// spells it.
func pinnedToolchain(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := modfile.ParseLax("go.mod", data, nil)
	if err != nil || parsed.Go == nil {
		t.Fatalf("go.mod names no Go release: %v", err)
	}

	return "go" + parsed.Go.Version
}

func localToolchain(t *testing.T) string {
	t.Helper()

	command := exec.CommandContext(t.Context(), "go", "env", "GOVERSION")

	command.Env = append(os.Environ(), "GOTOOLCHAIN=local")

	out, err := command.Output()
	if err != nil {
		t.Fatalf("go env GOVERSION: %v", err)
	}

	return strings.TrimSpace(string(out))
}

// Every package the CLI is built from lies under a build input, so the list
// the script prints names every source of the binary.
func TestBuildInputsCoverEveryPackageOfTheCLI(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)

	inputs, err := script(t.Context(), root, "inputs")
	if err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "go", "list", "-deps",
		"-f", "{{if .Module}}{{if or .Module.Main .Module.Replace}}{{.Dir}}{{end}}{{end}}", "./cmd/forge-ci")
	command.Dir = root

	command.Env = append(os.Environ(), "GOFLAGS=", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")

	out, err := command.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	packages := strings.Fields(string(out))
	if len(packages) == 0 {
		t.Fatal("go list named no package of this module; the check proves nothing")
	}

	for _, directory := range packages {
		relative, err := filepath.Rel(root, directory)
		if err != nil {
			t.Fatal(err)
		}

		covered := false

		for _, input := range strings.Fields(inputs) {
			if relative == input || strings.HasPrefix(relative, input+string(filepath.Separator)) {
				covered = true
			}
		}

		if !covered {
			t.Errorf("the CLI is built from %s, which no build input of %s covers", relative, binaryScript)
		}
	}
}
