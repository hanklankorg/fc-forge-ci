// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// fakeCargoTool records every Run call and synthesises responses for
// the cargo subcommands the cargo app code invokes: `metadata --no-deps`
// writes a stub JSON document to in.Stdout; `build --release …` creates
// the binary file at target/<triple>/release/<name> so locateCargoBinary
// succeeds without a real cargo on PATH.
type fakeCargoTool struct {
	calls       []appbuild.CargoRunInput
	metadata    string   // JSON to write on `metadata` calls
	binaryNames []string // candidates to write into target/release/ on `build` calls
	failOn      string
	runErr      error
}

func (f *fakeCargoTool) Run(_ context.Context, in appbuild.CargoRunInput) error {
	f.calls = append(f.calls, in)

	if len(in.Args) > 0 && in.Args[0] == "metadata" {
		if in.Stdout != nil {
			_, _ = io.WriteString(in.Stdout, f.metadata)
		}

		return nil
	}

	if len(in.Args) > 0 && in.Args[0] == f.failOn {
		return f.runErr
	}

	if len(in.Args) >= 5 && in.Args[0] == "build" {
		triple := ""

		for i, a := range in.Args {
			if a == "--target" && i+1 < len(in.Args) {
				triple = in.Args[i+1]
			}
		}

		if triple == "" {
			return nil
		}

		releaseDir := filepath.Join(in.Dir, "target", triple, "release")
		if err := os.MkdirAll(releaseDir, 0o755); err != nil {
			return err
		}

		for _, name := range f.binaryNames {
			path := filepath.Join(releaseDir, name)
			if err := os.WriteFile(path, []byte("ELFish:"+triple+":"+name), 0o755); err != nil { //nolint:gosec // test fixture
				return err
			}
		}
	}

	return nil
}

const sampleCargoMetadata = `{
  "packages": [
    {"name": "hello", "version": "0.4.2", "targets": [{"name": "hello", "kind": ["bin"]}]}
  ],
  "workspace_members": ["hello 0.4.2"],
  "workspace_root": ".",
  "resolve": null
}`

func TestCargoBuildBinaries_RejectsLinkedDistBeforeCleanup(t *testing.T) {
	for _, inside := range []bool{false, true} {
		project := t.TempDir()

		target := t.TempDir()
		if inside {
			target = filepath.Join(project, "source")
		}

		if err := os.MkdirAll(filepath.Join(target, "linux-amd64"), 0o700); err != nil {
			t.Fatal(err)
		}

		canary := filepath.Join(target, "linux-amd64", "keep")
		if err := os.WriteFile(canary, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(target, filepath.Join(project, "dist")); err != nil {
			t.Fatal(err)
		}

		tool := &fakeCargoTool{}

		err := appbuild.CargoBuildBinaries(t.Context(), tool, io.Discard, io.Discard, appbuild.CargoBuildBinariesInput{
			Dir: project, BinaryName: "app", CrateBinaryName: "app", Version: "1.0.0", Platforms: "linux/amd64",
		})
		if !errors.Is(err, errs.ErrValidation) || len(tool.calls) != 0 {
			t.Fatalf("linked dist admitted: %v, %+v", err, tool.calls)
		}

		if body, err := os.ReadFile(canary); err != nil || string(body) != "keep" {
			t.Fatalf("data changed: %q, %v", body, err)
		}
	}
}

func TestCargoMetadata_ResolvesBinaryVersionAndPackage(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)

	tool := &fakeCargoTool{metadata: sampleCargoMetadata}

	binaryName, version, packageName, err := appbuild.ResolveCargoMetadataForTest(context.Background(), tool, appbuild.CargoMetadataInput{Dir: fsys.Root, RefName: "v9.9.9"})
	if err != nil {
		t.Fatal(err)
	}

	if binaryName != "hello" {
		t.Errorf("binary-name = %q", binaryName)
	}

	if version != "9.9.9" {
		t.Errorf("version = %q (expected --ref-name to win over Cargo.toml)", version)
	}

	if packageName != "hello" {
		t.Errorf("package = %q", packageName)
	}
}

func TestCargoMetadata_FallsBackToCargoTomlVersion(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)

	tool := &fakeCargoTool{metadata: sampleCargoMetadata}

	_, version, _, err := appbuild.ResolveCargoMetadataForTest(context.Background(), tool, appbuild.CargoMetadataInput{Dir: fsys.Root})
	if err != nil {
		t.Fatal(err)
	}

	if version != "0.4.2" {
		t.Errorf("version = %q (expected Cargo.toml version)", version)
	}
}

func TestCargoFetch_RunsFetchLocked(t *testing.T) {
	t.Parallel()

	tool := &fakeCargoTool{}
	if err := appbuild.CargoFetch(context.Background(), tool, &bytes.Buffer{}, &bytes.Buffer{}, "src"); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(tool.calls[0].Args, " "); got != "fetch --locked" {
		t.Errorf("args = %q", got)
	}
}

func TestCargoTest_RunsAllTargets(t *testing.T) {
	t.Parallel()

	tool := &fakeCargoTool{}
	if err := appbuild.CargoTest(context.Background(), tool, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.CargoTestInput{Dir: "src"}); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(tool.calls[0].Args, " "); got != "test --locked --all-targets" {
		t.Errorf("args = %q", got)
	}
}

func TestCargoBuildBinaries_BuildsPlatforms(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)

	tool := &fakeCargoTool{metadata: sampleCargoMetadata, binaryNames: []string{"hello"}}

	if err := appbuild.CargoBuildBinaries(context.Background(), tool, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.CargoBuildBinariesInput{
		Dir:       fsys.Root,
		Platforms: "linux/amd64, linux/arm64",
		Version:   "1.2.3",
	}); err != nil {
		t.Fatal(err)
	}

	// One metadata call (binary-name resolution) + two build calls.
	if len(tool.calls) != 3 {
		t.Fatalf("expected 3 calls (metadata + 2 builds), got %d", len(tool.calls))
	}

	// Verify dist/ layout matches the Go shape.
	for _, want := range []string{
		"dist/linux-amd64/hello-linux-amd64",
		"dist/linux-arm64/hello-linux-arm64",
	} {
		if _, err := os.Stat(filepath.Join(fsys.Root, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}

	for platform, triple := range map[string]string{"linux-amd64": "x86_64-unknown-linux-gnu", "linux-arm64": "aarch64-unknown-linux-gnu"} {
		path := filepath.Join(fsys.Root, "dist", platform, "hello-"+platform)

		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		if string(body) != "ELFish:"+triple+":hello" {
			t.Errorf("%s has incorrect binary bytes", platform)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		if info.Mode().Perm() != 0o755 {
			t.Errorf("binary mode=%o", info.Mode().Perm())
		}
	}

	// Each platform is built for its own triple, in the declared order.
	// Collecting the triples into a "seen somewhere" set could not tell that
	// apart from building both platforms for one triple, or from swapping
	// them -- the output directories are named after the platform, not the
	// triple, so a swap would put an arm64 binary in dist/linux-amd64.
	wantTriples := []string{"x86_64-unknown-linux-gnu", "aarch64-unknown-linux-gnu"}

	gotTriples := make([]string, 0, len(tool.calls)-1)
	for _, call := range tool.calls[1:] {
		gotTriples = append(gotTriples, cargoTargetTriple(t, call.Args))
	}

	if !slices.Equal(gotTriples, wantTriples) {
		t.Errorf("build triples = %v, want %v (amd64 then arm64, as declared)", gotTriples, wantTriples)
	}
}

// cargoTargetTriple returns the value of --target in a recorded cargo call.
func cargoTargetTriple(t *testing.T, args []string) string {
	t.Helper()

	for i, arg := range args {
		if arg == "--target" && i+1 < len(args) {
			return args[i+1]
		}
	}

	t.Errorf("cargo called without --target: %v", args)

	return ""
}

func TestCargoBuildBinaries_RejectsUnknownPlatform(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)

	tool := &fakeCargoTool{metadata: sampleCargoMetadata, binaryNames: []string{"hello"}}

	err := appbuild.CargoBuildBinaries(context.Background(), tool, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.CargoBuildBinariesInput{
		Dir:       fsys.Root,
		Platforms: "plan9/amd64",
		Version:   "1.2.3",
	})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "unsupported platform") {
		t.Fatalf("err = %v, want ErrUsage naming the unsupported platform", err)
	}
}

func TestCargoBuildBinaries_RequiresVersion(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)

	tool := &fakeCargoTool{metadata: sampleCargoMetadata, binaryNames: []string{"hello"}}

	err := appbuild.CargoBuildBinaries(context.Background(), tool, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.CargoBuildBinariesInput{
		Dir:       fsys.Root,
		Platforms: "linux/amd64",
	})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "version is required") {
		t.Fatalf("err = %v, want ErrUsage naming the missing version", err)
	}
}

func TestCargoBuildBinaries_BinaryNameOverrideLocatesTheCrateBinary(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)

	// cargo always writes target/<triple>/release/<crate bin>; --binary-name
	// only renames the copy under dist/, so the crate's own name is what has
	// to be located.
	tool := &fakeCargoTool{metadata: sampleCargoMetadata, binaryNames: []string{"hello"}}

	if err := appbuild.CargoBuildBinaries(context.Background(), tool, &bytes.Buffer{}, &bytes.Buffer{}, appbuild.CargoBuildBinariesInput{
		Dir:        fsys.Root,
		BinaryName: "my-cli",
		Platforms:  "linux/amd64",
		Version:    "1.0.0",
	}); err != nil {
		t.Fatalf("build with --binary-name override: %v", err)
	}

	if _, err := os.Stat(filepath.Join(fsys.Root, "dist/linux-amd64/my-cli-linux-amd64")); err != nil {
		t.Fatalf("renamed dist binary missing: %v", err)
	}
}

// workspaceCargoMetadata mirrors what cargo 1.98 emits for a workspace whose
// root [package] ("zeta") sorts after a member ("alpha"): packages are
// ordered by id, resolve is null under --no-deps, and only manifest_path
// tells the root apart.
const workspaceCargoMetadata = `{
  "packages": [
    {"name": "alpha", "version": "0.2.0", "manifest_path": "/ws/alpha/Cargo.toml", "targets": [{"name": "alpha", "kind": ["bin"]}]},
    {"name": "zeta", "version": "0.1.0", "manifest_path": "/ws/Cargo.toml", "targets": [{"name": "zeta-cli", "kind": ["bin"]}]}
  ],
  "workspace_members": ["alpha 0.2.0", "zeta 0.1.0"],
  "workspace_root": "/ws",
  "resolve": null
}`

func TestCargoMetadata_PicksTheWorkspaceRootPackage(t *testing.T) {
	t.Parallel()
	fsys := testfs.NewReal(t)

	tool := &fakeCargoTool{metadata: workspaceCargoMetadata}

	binaryName, version, packageName, err := appbuild.ResolveCargoMetadataForTest(context.Background(), tool, appbuild.CargoMetadataInput{Dir: fsys.Root})
	if err != nil {
		t.Fatal(err)
	}

	if packageName != "zeta" {
		t.Errorf("package = %q, want the root package zeta", packageName)
	}

	if binaryName != "zeta-cli" {
		t.Errorf("binary-name = %q, want the root package's [[bin]] zeta-cli", binaryName)
	}

	if version != "0.1.0" {
		t.Errorf("version = %q, want the root package's 0.1.0", version)
	}
}
