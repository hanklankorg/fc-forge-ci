// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestRustReceiptSurvivesReducedToolset(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the Rust development fixture requires native Linux/AMD64")
	}

	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("Rust receipt regression needs Cargo")
	}

	busybox, err := exec.LookPath("busybox")
	if err != nil {
		t.Skip("Rust receipt regression needs BusyBox")
	}

	work := t.TempDir()

	const fixture = "test/live/fixtures/development/rust-development/consumer/"

	for _, name := range []string{"Cargo.toml", "Cargo.lock", "src/lib.rs", "examples/receipt.rs"} {
		destination := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(destination, reporoot.ReadFile(t, fixture+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	build := exec.CommandContext(t.Context(), cargo, "build", "--release", "--example", "receipt", "--locked", "--offline", "--target", "x86_64-unknown-linux-gnu")
	build.Dir = work
	build.Env = append(os.Environ(), "RUSTFLAGS=-C target-feature=+crt-static", "CARGO_TARGET_DIR="+filepath.Join(work, "target"))

	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static receipt writer: %v\n%s", err, output)
	}

	helper := filepath.Join(work, "target", "x86_64-unknown-linux-gnu", "release", "examples", "receipt")

	image, err := elf.Open(helper)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := image.Close(); err != nil {
			t.Error(err)
		}
	})

	for _, program := range image.Progs {
		if program.Type == elf.PT_INTERP {
			t.Fatal("receipt writer needs a dynamic loader that Kaniko removes")
		}
	}

	// The child's only external executable is BusyBox sha256sum: there is no
	// jq, Cargo, compiler or JSON utility on this PATH.
	tools := t.TempDir()
	if err := os.Symlink(busybox, filepath.Join(tools, "sha256sum")); err != nil {
		t.Fatal(err)
	}

	for _, width := range []int{40, 64} {
		source := strings.Repeat("a", width)
		version := "0.1.0-dev." + source[:12]
		directory := t.TempDir()
		files := rustArtifactFixture(t, rustFixtureRoles(), source)
		delete(files, "receipt.json")

		for name, body := range files {
			destination := filepath.Join(directory, name)
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(destination, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}

		run := func(sha, version string) error {
			cmd := exec.CommandContext(t.Context(), helper)
			cmd.Dir = directory
			cmd.Env = []string{"PATH=" + tools, "SOURCE_SHA=" + sha, "FIXTURE_VERSION=" + version}

			return cmd.Run()
		}
		if err := run(source, version); err != nil {
			t.Fatal(err)
		}

		original, err := os.ReadFile(filepath.Join(directory, "receipt.json"))
		if err != nil {
			t.Fatal(err)
		}

		var receipt struct {
			SourceSHA string            `json:"sourceSHA"`
			Version   string            `json:"version"`
			Files     map[string]string `json:"files"`
		}
		if err := json.Unmarshal(original, &receipt); err != nil {
			t.Fatal(err)
		}

		if receipt.SourceSHA != source || receipt.Version != version || len(receipt.Files) != 7 {
			t.Fatalf("wrong receipt bindings: %+v", receipt)
		}

		for name, body := range files {
			sum := sha256.Sum256(body)
			if receipt.Files[name] != hex.EncodeToString(sum[:]) {
				t.Fatalf("wrong checksum for %s", name)
			}
		}

		if run(`invalid"source`, version) == nil || run(source, "wrong-version") == nil {
			t.Fatal("invalid source/version accepted")
		}

		if err := os.Remove(filepath.Join(directory, "worker-image.json")); err != nil {
			t.Fatal(err)
		}

		if run(source, version) == nil {
			t.Fatal("missing artifact accepted")
		}

		after, err := os.ReadFile(filepath.Join(directory, "receipt.json"))
		if err != nil || string(after) != string(original) {
			t.Fatalf("failed generation replaced the previous receipt: %v", err)
		}
	}
}
