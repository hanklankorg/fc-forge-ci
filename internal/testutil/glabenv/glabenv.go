// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package glabenv sets up GitLab-CI-style environment for tests.
//
// GitLab has no per-step key=value sink equivalent to $GITHUB_OUTPUT;
// instead pipelines surface scalar outputs via `artifacts:reports:dotenv`
// (a file the script writes that GitLab parses as KEY=VALUE pairs).
//
// This helper:
//   - exports the canonical CI_* env vars (GITLAB_CI=true, CI_COMMIT_*,
//     CI_PROJECT_*, CI_PIPELINE_*) with sane defaults the test can override
//   - allocates a tempfile and points CI_OUTPUT at it (the platform-portable
//     name; the GitLab adapter writes dotenv format here)
package glabenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// Env wraps the CI_OUTPUT tempfile path for tests to inspect.
type Env struct {
	t          *testing.T
	OutputPath string
}

// Setenv forwards to the underlying test within the already-isolated env.
func (e *Env) Setenv(key, value string) {
	e.t.Helper()
	e.t.Setenv(key, value)
}

// Setup exports the CI_* env vars expected by the GitLab platform branch
// of internal/adapters/platform.Detect(), creates an empty CI_OUTPUT dotenv file,
// and returns a handle naming it.
func Setup(t *testing.T) *Env {
	t.Helper()
	env := testenv.New(t)
	dir := env.MkdirAll("glabenv")

	outPath := filepath.Join(dir, "output.env")
	if err := os.WriteFile(outPath, nil, 0o644); err != nil { //nolint:gosec // test infra; outPath is t.TempDir()-based.
		t.Fatalf("glabenv: create output: %v", err)
	}

	env.Setenv("GITLAB_CI", "true")
	env.Setenv("CI_OUTPUT", outPath)

	// Sane default context. Tests override individual values via t.Setenv.
	defaults := map[string]string{
		"CI_COMMIT_SHA":       "abcdef0123456789abcdef0123456789abcdef01",
		"CI_COMMIT_SHORT_SHA": "abcdef0",
		"CI_COMMIT_REF_NAME":  "main",
		"CI_COMMIT_TAG":       "",
		"CI_COMMIT_BRANCH":    "main",
		"CI_PROJECT_PATH":     "example/project",
		"CI_PROJECT_URL":      "https://gitlab.com/example/project",
		"CI_PIPELINE_ID":      "100",
		"CI_PIPELINE_URL":     "https://gitlab.com/example/project/-/pipelines/100",
		"CI_PIPELINE_SOURCE":  "push",
		"CI_SERVER_URL":       "https://gitlab.com",
		"GITLAB_USER_LOGIN":   "test-bot",
	}
	for k, v := range defaults {
		env.Setenv(k, v)
	}

	return &Env{t: t, OutputPath: outPath}
}
