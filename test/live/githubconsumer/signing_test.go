// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package githubconsumer_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

func TestReleaseSigningAndroidNonRelease(t *testing.T) {
	root := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-android/consumer")

	fixtures := "test/live/github"
	if _, err := os.Stat(filepath.Join(root, fixtures, "workflows/release.yml")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Android regression requires the real PR/build-only fixture: %v", err)
	}

	for _, event := range []string{livetest.TrialPullRequest, livetest.TrialDevRelease} {
		t.Run(event, func(t *testing.T) {
			settings, err := livetest.ParseGitHubTrialSettings(event, []string{"workflow=pullrequest.yml"})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := os.ReadFile(filepath.Join(root, fixtures, "workflows", settings.Workflow)); err != nil {
				t.Fatal(err)
			}

			key, principal, err := releaseSigning(root, fixtures, settings)
			if err != nil || key || principal != "" {
				t.Fatalf("non-release signing = %v, %q, %v", key, principal, err)
			}
		})
	}

	settings, err := livetest.ParseGitHubTrialSettings(livetest.TrialRelease, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := releaseSigning(root, fixtures, settings); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stable release must require its workflow: %v", err)
	}
}

func TestReleaseSigningSelectedWorkflow(t *testing.T) {
	for _, tc := range []struct {
		name, principal string
		email           any
		key, ssh        bool
		wantError       string
	}{
		{name: "keyless"},
		{name: "cosign", key: true},
		{name: "ssh", email: "release@example.org", ssh: true, principal: "release@example.org"},
		{name: "both", email: "release@example.org", ssh: true, key: true, principal: "release@example.org"},
		{name: "missing SSH principal", ssh: true, wantError: "literal bot-email"},
		{name: "wildcard", email: "*", ssh: true, wantError: "literal bot-email"},
		{name: "email wildcard", email: "*@example.org", ssh: true, wantError: "literal bot-email"},
		{name: "expression", email: "${{ vars.RELEASE_EMAIL }}", ssh: true, wantError: "literal bot-email"},
		{name: "display name", email: "Release Bot <release@example.org>", ssh: true, wantError: "literal bot-email"},
		{name: "principal list", email: "release@example.org,other@example.org", ssh: true, wantError: "literal bot-email"},
		{name: "nonstring", email: true, ssh: true, wantError: "literal bot-email"},
		{name: "newline", email: "release@example.org\nother@example.org", ssh: true, wantError: "literal bot-email"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()

			workflows := filepath.Join(root, "fixtures/workflows")
			if err := os.MkdirAll(workflows, 0o700); err != nil {
				t.Fatal(err)
			}

			// A contradictory conventional filename must not override the selector.
			if err := os.WriteFile(filepath.Join(workflows, "release.yml"), signingWorkflow(t, "*", true, false), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(workflows, "stable.yaml"), signingWorkflow(t, tc.email, tc.ssh, tc.key), 0o600); err != nil {
				t.Fatal(err)
			}

			settings, err := livetest.ParseGitHubTrialSettings(livetest.TrialRelease, []string{"workflow=stable.yaml"})
			if err != nil {
				t.Fatal(err)
			}

			key, principal, err := releaseSigning(root, "fixtures", settings)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("want %q, got %v", tc.wantError, err)
				}

				return
			}

			if err != nil || key != tc.key || principal != tc.principal {
				t.Fatalf("signing = %v, %q, %v; want %v, %q", key, principal, err, tc.key, tc.principal)
			}
		})
	}
}

func signingWorkflow(t *testing.T, email any, ssh, key bool) []byte {
	t.Helper()

	with := map[string]any{}
	secrets := map[string]string{}

	if email != nil {
		with["bot-email"] = email
	}

	if key {
		with["cosign-public-key-sha256"] = "digest"
	}

	if ssh {
		secrets["RELEASE_SSH_SIGNING_KEY"] = "${{ secrets.RELEASE_SSH_SIGNING_KEY }}"
	}

	body, err := yaml.Marshal(map[string]any{"jobs": map[string]any{"release": map[string]any{"with": with, "secrets": secrets}}})
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func TestReleaseSigningReadErrorsRemainFatal(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "fixtures/workflows/release.yml"), 0o700); err != nil {
		t.Fatal(err)
	}

	settings, err := livetest.ParseGitHubTrialSettings(livetest.TrialRelease, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = releaseSigning(root, "fixtures", settings)

	var pathError *fs.PathError
	if !errors.As(err, &pathError) || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("non-ENOENT read error must be preserved: %v", err)
	}
}

func TestReleaseSigningShippedReleaseFixtures(t *testing.T) {
	for _, tc := range []struct{ name, principal string }{
		{name: "hello-maven"},
		{name: "hello-go-cli", principal: "hello-release-bot@lab.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers", tc.name, "consumer")

			settings, err := livetest.ParseGitHubTrialSettings(livetest.TrialRelease, nil)
			if err != nil {
				t.Fatal(err)
			}

			key, principal, err := releaseSigning(root, "test/live/github", settings)
			if err != nil || key || principal != tc.principal {
				t.Fatalf("shipped signing = %v, %q, %v; want keyless Cosign, SSH principal %q", key, principal, err, tc.principal)
			}
		})
	}
}

func TestReleaseSigningRejectsUnsafeWorkflowSelectors(t *testing.T) {
	for _, event := range []string{livetest.TrialPullRequest, livetest.TrialDevRelease, livetest.TrialRelease} {
		for _, workflow := range []string{"../release.yml", "/release.yml", "sub/release.yml", "release.sh"} {
			if _, err := livetest.ParseGitHubTrialSettings(event, []string{"workflow=" + workflow}); err == nil {
				t.Errorf("accepted unsafe selector %q for %s", workflow, event)
			}
		}
	}
}

// These are ordinary YAML anchors, not a separate fixture language. Reuse the
// same documents at the actual runTrial/SDK allocation boundary.
var signingAliasCases = []struct{ name, body, failure string }{
	{"secrets-map-invalid", `jobs:
  first:
    secrets: &signing
      RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
    with:
      bot-email: release@example.org
  second:
    secrets: *signing
    with:
      bot-email: '*'
`, "literal bot-email"},
	{"secrets-map-valid", `signing: &signing
  RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
jobs:
  release:
    secrets: *signing
    with:
      bot-email: release@example.org
`, ""},
	{"secret-value-invalid", `signing: &signing ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
jobs:
  release:
    secrets:
      RELEASE_SSH_SIGNING_KEY: *signing
    with:
      bot-email: '*'
`, "literal bot-email"},
	{"secret-value-valid", `signing: &signing ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
jobs:
  release:
    secrets:
      RELEASE_SSH_SIGNING_KEY: *signing
    with:
      bot-email: release@example.org
`, ""},
	{"principal-invalid", `email: &email '${{ vars.EMAIL }}'
jobs:
  release:
    secrets:
      RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
    with:
      bot-email: *email
`, "literal bot-email"},
	{"principal-valid", `email: &email release@example.org
jobs:
  release:
    secrets:
      RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
    with:
      bot-email: *email
`, ""},
	{"with-map-invalid", `identity: &identity
  bot-email: '*'
jobs:
  release:
    secrets:
      RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
    with: *identity
`, "literal bot-email"},
	{"with-map-valid", `identity: &identity
  bot-email: release@example.org
jobs:
  release:
    secrets:
      RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
    with: *identity
`, ""},
	{"job-invalid", `signing: &signing
  secrets:
    RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
  with:
    bot-email: '*'
jobs:
  release: *signing
`, "literal bot-email"},
	{"job-valid", `signing: &signing
  secrets:
    RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
  with:
    bot-email: release@example.org
jobs:
  release: *signing
`, ""},
	{"cyclic-secrets", `jobs:
  release:
    secrets: &signing
      RELEASE_SSH_SIGNING_KEY: *signing
    with:
      bot-email: release@example.org
`, "decode release job"},
	{"cyclic-with", `jobs:
  release:
    secrets:
      RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
    with: &identity
      bot-email: *identity
`, "parse the fixture"},
	{"undefined", `jobs:
  release:
    secrets: *missing
`, "parse the fixture"},
}

func TestReleaseSigningAliases(t *testing.T) {
	settings, err := livetest.ParseGitHubTrialSettings(livetest.TrialRelease, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range signingAliasCases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()

			workflows := filepath.Join(root, "fixtures/workflows")
			if err := os.MkdirAll(workflows, 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(workflows, "release.yml"), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}

			key, principal, err := releaseSigning(root, "fixtures", settings)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("want %q, got %v", tc.failure, err)
				}
			} else if err != nil || key || principal != "release@example.org" {
				t.Fatalf("alias signing = %v, %q, %v", key, principal, err)
			}
		})
	}
}

// A shipped keyless release is re-keyed by the trial; the GPG variants, which
// write to no public log, are left as they are, and so is a pull request,
// which signs nothing.
func TestKeylessReleaseOfShippedFixtures(t *testing.T) {
	demo := func(name string) string {
		return filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers", name, "consumer")
	}

	for _, tc := range []struct {
		demo, event, fixtures string
		words                 []string
		config                string
	}{
		{demo: "hello-maven-service", event: livetest.TrialRelease, fixtures: "test/live/github", config: ".forge-ci/artifacts.yml"},
		{demo: "hello-maven-service", event: livetest.TrialLifecycle, fixtures: "test/live/github", config: ".forge-ci/artifacts.yml"},
		{demo: "hello-meta", event: livetest.TrialRelease, fixtures: "test/live/github", config: ".forge-ci/artifacts.yml"},
		{demo: "hello-maven-service", event: livetest.TrialPullRequest, fixtures: "test/live/github"},
		{demo: "hello-go-cli", event: livetest.TrialRelease, fixtures: "test/live/github-gpg", words: []string{"workflow=release-gpg.yml"}},
		{demo: "hello-go-cli", event: livetest.TrialRelease, fixtures: "test/live/github-no-sboms", words: []string{"workflow=release-no-sboms.yml"}},
		// GPG files beside a Cosign-key image: declared with a key and no log.
		{demo: "hello-go-service", event: livetest.TrialRelease, fixtures: "test/live/github-gpg-files", words: []string{"workflow=release-gpg-files.yml", "image=my-service"}},
	} {
		settings, err := livetest.ParseGitHubTrialSettings(tc.event, tc.words)
		if err != nil {
			t.Fatal(err)
		}

		config, _, err := keylessRelease(demo(tc.demo), tc.fixtures, settings)
		if err != nil || config != tc.config {
			t.Errorf("%s %s %s: keyless config %q, %v; want %q", tc.demo, tc.event, tc.fixtures, config, err, tc.config)
		}
	}
}
