// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build integration

package version_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cargoadapter "github.com/diggsweden/forge-ci/internal/adapters/cargo"
	gitadapter "github.com/diggsweden/forge-ci/internal/adapters/git"
	npmadapter "github.com/diggsweden/forge-ci/internal/adapters/npm"
	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

func TestIsolatedVersionBump_TwoNativeNPMCargoRounds(t *testing.T) {
	for _, kind := range []projecttype.Type{projecttype.NPM, projecttype.Cargo} {
		t.Run(string(kind), func(t *testing.T) {
			tool := string(kind)
			if kind == projecttype.Cargo {
				// cargo on rustup runners is a proxy shim; verify it runs.
				evidence.RequireWorkingTool(t, tool, "--version")
			} else {
				evidence.RequireTool(t, tool)
			}
			repo := isolatedgit.NewRepo(t)
			config := filepath.Join(t.TempDir(), "empty-npmrc")
			if err := os.WriteFile(config, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NPM_CONFIG_USERCONFIG", config)
			globalConfig := filepath.Join(t.TempDir(), "global-npmrc")
			if err := os.WriteFile(globalConfig, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NPM_CONFIG_GLOBALCONFIG", globalConfig)
			t.Setenv("CARGO_HOME", t.TempDir())
			if kind == projecttype.NPM {
				repo.AddFile("package.json", "{\n  \"name\": \"version-canary\",\n  \"version\": \"1.2.2\",\n  \"private\": true,\n  \"engines\": { \"node\": \">=18\" }\n}\n", "chore: package")
				repo.AddFile("package-lock.json", "{\n  \"name\": \"version-canary\",\n  \"version\": \"1.2.2\",\n  \"lockfileVersion\": 3,\n  \"requires\": true,\n  \"packages\": {\n    \"\": {\n      \"name\": \"version-canary\",\n      \"version\": \"1.2.2\",\n      \"engines\": { \"node\": \">=18\" }\n    }\n  }\n}\n", "chore: lock")
			} else {
				repo.AddFile("Cargo.toml", "[workspace]\nmembers = [\"worker\", \"keytool\"]\nresolver = \"2\"\n[workspace.package]\nversion = \"1.2.2\"\nedition = \"2021\"\n", "chore: workspace")
				for _, name := range []string{"worker", "keytool"} {
					repo.AddFile(name+"/Cargo.toml", "[package]\nname = \""+name+"\"\nversion.workspace = true\nedition.workspace = true\n", "chore: member")
					repo.AddFile(name+"/src/main.rs", "fn main() {}\n", "chore: source")
				}
				command := exec.CommandContext(t.Context(), "cargo", "generate-lockfile", "--offline")
				command.Dir = repo.Dir
				if result, err := command.CombinedOutput(); err != nil {
					t.Fatalf("generate fixture lock: %v\n%s", err, result)
				}
				repo.Git("add", "Cargo.lock")
				repo.Git("commit", "-m", "chore: lock workspace")
			}
			t.Chdir(repo.Dir)
			for _, release := range []string{"1.2.3", "1.2.4"} {
				var diagnostic evidence.Diagnostic
				if err := appversion.Bump(t.Context(), appversion.BumpOps{NPM: npmadapter.New(), Cargo: cargoadapter.New()}, io.Discard, &diagnostic, output.Annotator{}, appversion.BumpInput{ProjectType: kind, Version: release, WorkingDir: "."}); err != nil {
					t.Fatalf("%v\nstderr: %s", err, diagnostic.Text(os.Environ()))
				}
				patch := []byte(repo.Git("diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/") + "\n")
				repo.Git("checkout", "--", ".")
				stage := t.TempDir()
				patchFile, pathsFile := filepath.Join(stage, "bump.patch"), filepath.Join(stage, "paths")
				if err := os.WriteFile(patchFile, patch, 0o600); err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(patch)
				result, err := appversion.ApplyBump(t.Context(), gitadapter.New(), io.Discard, appversion.ApplyBumpInput{PatchPath: patchFile, SHA256: hex.EncodeToString(sum[:]), ProjectType: kind, Version: release, WorkingDir: ".", PathsFile: pathsFile})
				if err != nil {
					t.Fatalf("%s native bump to %s refused: %v", kind, release, err)
				}
				if len(result.Paths) != 2 {
					t.Fatalf("manifest and lockfile must both participate: %v", result.Paths)
				}
				repo.Git("add", ".")
				repo.Git("commit", "-m", "chore: release "+release)
			}
		})
	}
}

func TestIsolatedVersionBump_RejectsVersionShapedScriptChangesWithoutPublishingPaths(t *testing.T) {
	repo := isolatedgit.NewRepo(t)
	const original = "{\n  \"name\": \"fixture\",\n  \"version\": \"1.2.2\",\n  \"scripts\": {\"release\": \"echo 1.2.2\"}\n}\n"
	repo.AddFile("package.json", original, "chore: package")
	head := repo.Git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo.Dir, "package.json"), []byte(strings.ReplaceAll(original, "1.2.2", "1.2.3")), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := []byte(repo.Git("diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/") + "\n")
	repo.Git("checkout", "--", ".")
	directory := t.TempDir()
	patchFile, pathsFile := filepath.Join(directory, "bump.patch"), filepath.Join(directory, "must-not-exist")
	if err := os.WriteFile(patchFile, patch, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(patch)
	t.Chdir(repo.Dir)
	_, err := appversion.ApplyBump(t.Context(), gitadapter.New(), io.Discard, appversion.ApplyBumpInput{PatchPath: patchFile, SHA256: hex.EncodeToString(sum[:]), ProjectType: projecttype.NPM, Version: "1.2.3", WorkingDir: ".", PathsFile: pathsFile})
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("script change disguised as a version bump was accepted: %v", err)
	}
	if _, err := os.Stat(pathsFile); !os.IsNotExist(err) {
		t.Fatalf("invalid bump published release paths: %v", err)
	}
	if repo.Git("rev-parse", "HEAD") != head {
		t.Fatal("invalid bump advanced the source commit")
	}
}
