// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package doctor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/app/doctor"
)

// A repository with a valid configuration and no CI file runs nothing; doctor
// used to pass it and say to open a pull request.
func TestRun_NoCIWorkflowsFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".forge-ci"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, ".forge-ci", "artifacts.yml"), []byte("artifacts:\n  - name: widget\n    project-type: meta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, doctor.CheckCIWorkflows)
	if c == nil || c.Severity != doctor.SeverityFail || !strings.Contains(c.Remediation, "forge-ci init") {
		t.Fatalf("CI check = %+v, want a FAIL pointing at forge-ci init", c)
	}

	// artifacts.yml is still scanned; only with no Forge CI file at all is
	// there nothing to call "filled in".
	if c := findCheck(checks, doctor.CheckPlaceholders); c == nil || c.Severity != doctor.SeverityOK {
		t.Errorf("placeholder check with artifacts.yml only = %+v, want OK", c)
	}

	empty, err := doctor.Run(doctor.Input{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	if c := findCheck(empty, doctor.CheckPlaceholders); c != nil {
		t.Errorf("an empty repository got a placeholder verdict: %+v", c)
	}

	if doctor.ExitCode(checks) == 0 {
		t.Error("a repository with no CI must not pass doctor")
	}
}

// The values init leaves for a person parse and validate; doctor names each
// with its file and line and how to fill it, and fails until they are gone.
func TestRun_UnfilledExampleValuesFail(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, "artifacts:\n  - name: my-cli\n    project-type: meta\n", map[string]string{
		".github/workflows/release-workflow.yml": "jobs:\n  release:\n    with:\n      # bot-email: FIXME is explained here\n" +
			"      bot-email: FIXME-release-bot@example.com\n      nanolinter-image: ghcr.io/itiquette/nanolinter:go@sha256:PIN_DIGEST\n" +
			"      registry: \"Destination host (e.g. harbor.example.com)\"\n    uses: diggsweden/forge-ci/.github/workflows/release-workflow.yml@v1\n",
	})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, doctor.CheckPlaceholders)
	if c == nil || c.Severity != doctor.SeverityFail {
		t.Fatalf("placeholder check = %+v, want FAIL", c)
	}

	for _, want := range []string{
		".forge-ci/artifacts.yml:2 my-cli",
		".github/workflows/release-workflow.yml:5 FIXME-release-bot",
		".github/workflows/release-workflow.yml:6 PIN_DIGEST",
	} {
		if !strings.Contains(c.Message, want) {
			t.Errorf("message %q does not name %q", c.Message, want)
		}
	}

	if strings.Contains(c.Message, ":4 ") || strings.Contains(c.Message, "harbor.example.com") {
		t.Errorf("a comment line was reported: %q", c.Message)
	}

	if !strings.Contains(c.Remediation, "forge-ci container manifest digest") || !strings.Contains(c.Remediation, "forge-ci setup signer") {
		t.Errorf("remediation must say how to fill each kind; got %q", c.Remediation)
	}
}

// The example's ./cmd/my-cli validates and fails the build in CI; doctor
// catches it, and a missing working directory, locally.
func TestRun_MissingArtifactPathsFail(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, `
artifacts:
  - name: widget
    project-type: go
    config:
      main-package: ./cmd/widget
  - name: lib
    project-type: meta
    working-directory: services/lib
`, map[string]string{"go.mod": "module example.com/widget\n", "main.go": "package main\n"})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, doctor.CheckArtifactPaths)
	if c == nil || c.Severity != doctor.SeverityFail {
		t.Fatalf("path check = %+v, want FAIL", c)
	}

	for _, want := range []string{"widget: main-package ./cmd/widget", "lib: working-directory services/lib"} {
		if !strings.Contains(c.Message, want) {
			t.Errorf("message %q does not name %q", c.Message, want)
		}
	}

	if err := os.MkdirAll(filepath.Join(root, "cmd", "widget"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "cmd", "widget", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "services", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}

	checks, err = doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	if c := findCheck(checks, doctor.CheckArtifactPaths); c == nil || c.Severity != doctor.SeverityOK {
		t.Errorf("path check after creating them = %+v, want OK", c)
	}
}

// A variable named like an administrator setting makes every gate refuse to
// run; doctor fails there too instead of passing a repository whose gates
// cannot start.
func TestRun_AdminSettingInTheEnvironmentFails(t *testing.T) {
	t.Setenv("CI_ENFORCE_LINT", "true") // not parallel: changes the process environment

	checks, err := doctor.Run(doctor.Input{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	c := findCheck(checks, doctor.CheckAdminPolicy)
	if c == nil || c.Severity != doctor.SeverityFail || !strings.Contains(c.Message, "CI_ENFORCE_LINT") {
		t.Fatalf("admin policy check = %+v, want a FAIL naming the variable", c)
	}
}

// Values left only in a release workflow do not stop a pull request, so they
// warn; one in a file a pull request runs fails.
func TestRun_ReleaseOnlyExampleValuesWarn(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, "artifacts:\n  - name: widget\n    project-type: meta\n", map[string]string{
		".github/workflows/release-workflow.yml": "jobs:\n  release:\n    with:\n      bot-email: FIXME-release-bot@example.com\n    uses: diggsweden/forge-ci/.github/workflows/release-workflow.yml@v1\n",
	})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	if c := findCheck(checks, doctor.CheckPlaceholders); c == nil || c.Severity != doctor.SeverityWarn || !strings.Contains(c.Message, "release workflow only") {
		t.Fatalf("release-only values = %+v, want a WARN", c)
	}

	if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "ci.yml"), []byte("uses: diggsweden/forge-ci/.github/actions/lint@v1\nimage: x@sha256:PIN_DIGEST\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	checks, err = doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	if c := findCheck(checks, doctor.CheckPlaceholders); c == nil || c.Severity != doctor.SeverityFail {
		t.Fatalf("a value in a pull-request file = %+v, want a FAIL", c)
	}
}

// A workflow that does not call forge-ci is the repository's own: init never
// wrote it, so its FIXME or PIN_ variable is not a value left to fill.
func TestRun_OtherWorkflowsAreNotScannedForExampleValues(t *testing.T) {
	t.Parallel()

	root := writeRepo(t, "artifacts:\n  - name: widget\n    project-type: meta\n", map[string]string{
		".github/workflows/lint.yml": "env:\n  PIN_NODE_VERSION: 20\njobs:\n  todo:\n    steps:\n      - run: grep -rn FIXME src/\n",
	})

	checks, err := doctor.Run(doctor.Input{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	if c := findCheck(checks, doctor.CheckPlaceholders); c == nil || c.Severity != doctor.SeverityOK {
		t.Fatalf("placeholder check = %+v, want OK", c)
	}
}
