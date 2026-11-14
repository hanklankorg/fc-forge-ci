// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	plancmd "github.com/diggsweden/forge-ci/internal/cli/commands/plan"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const releaseVersionsDeclaration = `artifacts:
  - name: backend-api
    project-type: maven
    working-directory: services/backend
  - name: frontend-ui
    project-type: npm
    working-directory: packages/frontend
  - name: tool
    project-type: go
    working-directory: tools/cli
`

// The version plan names one matrix row a family with a native version tool,
// the same families one a line, and the prepare plan; a Go project has no
// version file, so it adds no row.
func TestReleaseVersionsCmd_WritesOneRowPerNativeFamily(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	declaration := filepath.Join(dir, "artifacts.yml")
	matrix, families, prepare := filepath.Join(dir, "matrix.json"), filepath.Join(dir, "families"), filepath.Join(dir, "prepare.json")

	if err := os.WriteFile(declaration, []byte(releaseVersionsDeclaration), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := plancmd.New().Run(t.Context(), []string{
		"plan", "release-versions", "--file", declaration,
		"--runtime-image-java", "registry.example/java@sha256:" + strings.Repeat("1", 64),
		"--runtime-image-node", "registry.example/node@sha256:" + strings.Repeat("2", 64),
		"--matrix-output", matrix, "--families-output", families, "--prepare-plan-output", prepare,
	}); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		matrix:   `[{"family":"java","project-type":"maven"},{"family":"node","project-type":"npm"}]` + "\n",
		families: "java\nnode\n",
	} {
		if body, err := os.ReadFile(path); err != nil || string(body) != want {
			t.Errorf("%s = %q, %v; want %q", filepath.Base(path), body, err, want)
		}
	}

	if body, err := os.ReadFile(prepare); err != nil || !strings.Contains(string(body), `"name": "tool"`) {
		t.Errorf("prepare plan %s, %v; want every planned project", body, err)
	}
}

// A planned family whose image the caller did not give stops the plan before
// any version job is scheduled; the families list, which runs nothing, does
// not need the images.
func TestReleaseVersionsCmd_RefusesAPlannedFamilyWithoutItsImage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	declaration := filepath.Join(dir, "artifacts.yml")

	if err := os.WriteFile(declaration, []byte(releaseVersionsDeclaration), 0o600); err != nil {
		t.Fatal(err)
	}

	err := plancmd.New().Run(t.Context(), []string{
		"plan", "release-versions", "--file", declaration,
		"--runtime-image-java", "registry.example/java@sha256:" + strings.Repeat("1", 64),
		"--matrix-output", filepath.Join(dir, "matrix.json"),
	})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "runtime-image-node") {
		t.Fatalf("err = %v, want the missing runtime-image-node named", err)
	}

	if err := plancmd.New().Run(t.Context(), []string{
		"plan", "release-versions", "--file", declaration, "--families-output", filepath.Join(dir, "families"),
	}); err != nil {
		t.Fatalf("the families list needs no image: %v", err)
	}
}

// Without a declaration the release bumps nothing: an empty matrix and no
// family. A declaration named but absent is an error.
//
//nolint:paralleltest // t.Chdir: the default declaration is relative to the working directory.
func TestReleaseVersionsCmd_WithoutADeclarationPlansNoFamily(t *testing.T) {
	dir := t.TempDir()
	matrix, families := filepath.Join(dir, "matrix.json"), filepath.Join(dir, "families")

	t.Chdir(dir)

	if err := plancmd.New().Run(t.Context(), []string{
		"plan", "release-versions", "--matrix-output", matrix, "--families-output", families,
	}); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{matrix: "[]\n", families: ""} {
		if body, err := os.ReadFile(path); err != nil || string(body) != want {
			t.Errorf("%s = %q, %v; want %q", filepath.Base(path), body, err, want)
		}
	}

	err := plancmd.New().Run(t.Context(), []string{
		"plan", "release-versions", "--file", filepath.Join(dir, "absent.yml"), "--families-output", families,
	})
	if !errors.Is(err, errs.ErrMissingInput) {
		t.Fatalf("err = %v, want a missing named declaration refused", err)
	}
}
