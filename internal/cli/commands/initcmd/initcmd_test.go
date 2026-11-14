// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package initcmd

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domain "github.com/diggsweden/forge-ci/internal/domain/consumerinit"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

type fixtureRemote struct{}

func (fixtureRemote) VersionTags(context.Context, string) ([]string, error) {
	return []string{"v1.2.3"}, nil
}

func (fixtureRemote) TagCommit(context.Context, string, string) (string, error) {
	return strings.Repeat("a", 40), nil
}

func (fixtureRemote) Export(_ context.Context, _, _, dest string, _ []string) ([]string, error) {
	dir := filepath.Join(dest, "examples", "go-cli", "github")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}

	if err := os.WriteFile(filepath.Join(dir, "pullrequest-workflow.yml"), []byte("name: my-cli\nref: v0.90.0\n"), 0o600); err != nil {
		return nil, err
	}

	if err := os.WriteFile(filepath.Join(dir, "release-workflow.yml"), []byte("name: my-cli\n"), 0o600); err != nil {
		return nil, err
	}

	return []string{domain.DefaultsFile}, nil
}

func TestInitCommandDryRun(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "new_files"
		if conflict {
			name = "existing_workflow"
		}

		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			existing := filepath.Join(dir, ".github", "workflows", "pullrequest-workflow.yml")
			if conflict {
				if err := os.MkdirAll(filepath.Dir(existing), 0o750); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(existing, []byte("keep this workflow\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			var output bytes.Buffer

			cmd := newCommand(fixtureRemote{})
			cmd.Writer, cmd.ErrWriter = &output, &output

			err := cmd.Run(t.Context(), []string{"init", "https://github.com/fixture/forge-ci", dir, "--example", "go-cli", "--ref", "v1.2.3", "--dry-run"})
			if conflict != errors.Is(err, errs.ErrUsage) || (!conflict && err != nil) {
				t.Fatalf("conflict=%t: unexpected error: %v", conflict, err)
			}

			for _, expected := range []string{
				"Forge CI initialization preview", "Project directory: " + dir,
				"Template source: https://github.com/fixture/forge-ci", "Release: v1.2.3",
				"Resolved commit: " + strings.Repeat("a", 40), "Forge: github", "Example: go-cli",
				".github/workflows/release-workflow.yml", "my-cli", "No project files were written",
				"temporary storage", "no Nanolinter or MegaLinter configuration is generated",
			} {
				if !strings.Contains(output.String(), expected) {
					t.Errorf("missing %q in preview:\n%s", expected, output.String())
				}
			}

			if strings.Contains(output.String(), "Release setup: run forge-ci doctor --requirements") {
				t.Fatalf("preview must not imply initialization is complete: %s", output.String())
			}

			if conflict {
				if !strings.Contains(output.String(), "generation blocked") || !strings.Contains(output.String(), "Other proposed files") || strings.Contains(output.String(), "repeat the command") {
					t.Fatalf("misleading conflict output: %s", output.String())
				}

				body, readErr := os.ReadFile(existing)
				if readErr != nil || string(body) != "keep this workflow\n" {
					t.Fatalf("existing workflow changed: %q, %v", body, readErr)
				}
			} else if !strings.Contains(output.String(), "Would create") || !strings.Contains(output.String(), "repeat the command without --dry-run") {
				t.Fatalf("missing next step: %s", output.String())
			}

			if _, statErr := os.Lstat(filepath.Join(dir, ".github", "workflows", "release-workflow.yml")); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("preview wrote a proposed file: %v", statErr)
			}
		})
	}
}

func TestInitCommandWithoutDryRunCreatesFiles(t *testing.T) {
	dir := t.TempDir()

	var output bytes.Buffer

	cmd := newCommand(fixtureRemote{})
	cmd.Writer, cmd.ErrWriter = &output, &output

	err := cmd.Run(t.Context(), []string{"init", "https://github.com/fixture/forge-ci", dir, "--example", "go-cli", "--ref", "v1.2.3"})
	if err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "pullrequest-workflow.yml"))
	if err != nil || string(body) != "name: my-cli\nref: v1.2.3\n" {
		t.Fatalf("rendered workflow: %q, %v", body, err)
	}

	if !strings.Contains(output.String(), "Created:") || strings.Contains(output.String(), "No project files were written") {
		t.Fatalf("misleading generation report: %s", output.String())
	}

	for _, expected := range []string{
		"Getting started section of https://github.com/fixture/forge-ci/blob/v1.2.3/docs/github.md",
		"then run forge-ci setup status for the remaining steps",
		"(offline: forge-ci docs github)",
		"Release setup: run forge-ci doctor --requirements",
		"remote secrets and permissions are not checked",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("missing documentation handoff %q: %s", expected, output.String())
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "docs")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("initialization should not imply a local documentation tree: %v", err)
	}
}

// Build files only below the root are a repository with several artifacts:
// init names them and the docs section for it instead of "no build file".
func TestChooseExampleNamesNestedProjects(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, f := range []string{"api/go.mod", "web/package.json", ".hidden/go.mod"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	in, err := os.Open(filepath.Join(dir, "api", "go.mod")) // not a terminal
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = in.Close() })

	_, err = chooseExample("", dir, in, &bytes.Buffer{})
	if !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("err = %v, want a usage error", err)
	}

	for _, want := range []string{"api (go)", "web (npm)", "artifacts-reference#monorepo-configuration"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	if strings.Contains(err.Error(), ".hidden") {
		t.Errorf("a hidden directory was reported: %v", err)
	}
}

// A detected name init refused is reported as not filled, not as filled.
func TestReportProjectSaysWhenANameWasNotFilled(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	reportProject(&out, domain.Project{Name: "MyService"})

	if got := out.String(); !strings.Contains(got, "MyService (not filled") {
		t.Errorf("report = %q, want the name marked not filled", got)
	}

	out.Reset()
	reportProject(&out, domain.Project{Name: "hello", MainPackage: "."})

	if got := out.String(); strings.Contains(got, "not filled") || !strings.Contains(got, "project name   hello\n") {
		t.Errorf("report = %q, want hello reported as filled", got)
	}
}
