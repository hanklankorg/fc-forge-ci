// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// Release components run under GNU coreutils on Linux. Expose those tools
// explicitly without importing the developer's full PATH into isolated scripts.
func componentTools(t *testing.T) string {
	t.Helper()

	return testenv.GNUPath(t, "cp", "chmod", "wc", "sha256sum") + ":/usr/bin:/bin"
}

// Execute the actual component's guard. A fake Git records any attempted
// checkout; all files stay in the test directory and no network tool is called.
func TestCheckRefusesSigningMaterialAndMutablePinsBeforeCheckout(t *testing.T) {
	t.Parallel()
	bootstrap := checkBootstrapScript(t)

	const canary = "fixture-value-must-not-appear-in-output"

	for _, tc := range []struct {
		name, variable, reference, want string
	}{

		{"SSH key", "RELEASE_SSH_SIGNING_KEY", strings.Repeat("a", 40), "RELEASE_SSH_SIGNING_KEY must not be available"},
		{"Cosign key", "RELEASE_COSIGN_PRIVATE_KEY", strings.Repeat("a", 40), "RELEASE_COSIGN_PRIVATE_KEY must not be available"},
		{"GPG key", "RELEASE_GPG_PRIVATE_KEY", strings.Repeat("a", 40), "RELEASE_GPG_PRIVATE_KEY must not be available"},
		{"release token", "RELEASE_TOKEN", strings.Repeat("a", 40), "RELEASE_TOKEN must not be available"},
		{"GitLab API token", "GITLAB_TOKEN", strings.Repeat("a", 40), "GITLAB_TOKEN must not be available"},
		{"mutable pin", "", "main", "pin the check component by its full commit SHA"},
		{"malformed pipeline commit", "", strings.Repeat("a", 40), "CI_COMMIT_SHA must be a full SHA-1 or SHA-256 commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			marker := filepath.Join(dir, "checkout-attempted")

			if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\ntouch \"$MARKER\"\nexit 93\n"), 0o700); err != nil { //nolint:gosec // Owner-only executable fixture in t.TempDir, with no credential data.
				t.Fatal(err)
			}

			script := strings.NewReplacer(
				"$[[ component.sha ]]", strings.Repeat("a", 40),
				"$[[ component.reference ]]", tc.reference,
				"/tmp/forge-ci-helpers.XXXXXXXX", filepath.Join(dir, "helpers.XXXXXXXX"),
			).Replace(bootstrap)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)

			defer cancel()

			command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-c", script) //nolint:gosec // Reviewed component shell, closed environment and an inert Git executable.
			command.Dir = dir
			command.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "MARKER=" + marker,
				"CI_SERVER_URL=https://gitlab.invalid", "RC_FORGE_CI_REPOSITORY=owner/engine", "CI_COMMIT_SHA=not-a-commit"}

			if tc.variable != "" {
				command.Env = append(command.Env, tc.variable+"="+canary)
			}

			output, runErr := command.CombinedOutput()
			if runErr == nil || !strings.Contains(string(output), tc.want) || strings.Contains(string(output), canary) {
				t.Fatalf("guard result=%v; expected a non-disclosing refusal naming %q", runErr, tc.want)
			}

			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a refused check attempted checkout")
			}
		})
	}
}

func checkBootstrapScript(t *testing.T) string {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(reporoot.ReadFile(t, "templates/check.yml")))
	var header any
	if err := decoder.Decode(&header); err != nil {
		t.Fatal(err)
	}
	var jobs map[string]struct {
		Before []string `yaml:"before_script"`
	}
	if err := decoder.Decode(&jobs); err != nil {
		t.Fatal(err)
	}
	job := jobs["$[[ inputs.job-name ]]"]
	if len(job.Before) != 1 {
		t.Fatal("check component must have its bootstrap guard")
	}
	return job.Before[0]
}

func TestCheckPreservesPublicRootsAndExportsPrivateRegistryTrust(t *testing.T) {
	bootstrap := checkBootstrapScript(t)
	_, trust, found := strings.Cut(bootstrap, "git_options=")
	if !found {
		t.Fatal("missing trust setup")
	}
	trust, _, found = strings.Cut(trust, "# The consumer may default Git")
	if !found {
		t.Fatal("missing end of trust setup")
	}
	for _, privateCA := range []bool{false, true} {
		t.Run(map[bool]string{false: "public roots unchanged", true: "private registry plus public roots"}[privateCA], func(t *testing.T) {
			dir := t.TempDir()
			public, private := filepath.Join(dir, "public.pem"), filepath.Join(dir, "private.pem")
			if err := os.WriteFile(public, []byte("public-root\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(private, []byte("lab-root\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			script := strings.NewReplacer("/etc/ssl/certs/ca-certificates.crt", public,
				"/etc/pki/tls/certs/ca-bundle.crt", filepath.Join(dir, "absent.pem")).Replace("git_options=" + trust)
			command := exec.CommandContext(t.Context(), "/bin/bash", "--noprofile", "--norc", "-c",
				"set -eu\n"+script+"\nprintf '%s\\n' \"${SSL_CERT_FILE:-}\" \"${CURL_CA_BUNDLE:-}\" \"${GIT_SSL_CAINFO:-}\"")
			command.Env = []string{"PATH=/usr/bin:/bin", "RC_HELPERS_DIR=" + dir}
			if privateCA {
				command.Env = append(command.Env, "CI_SERVER_TLS_CA_FILE="+private)
			}
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("trust setup: %v\n%s", err, output)
			}
			if !privateCA {
				if strings.TrimSpace(string(output)) != "" {
					t.Fatalf("public roots overridden: %s", output)
				}
				return
			}
			bundle := filepath.Join(dir, "ca-bundle.crt")
			if string(output) != strings.Repeat(bundle+"\n", 3) {
				t.Fatalf("trust not exported for consumer commands: %s", output)
			}
			body, err := os.ReadFile(bundle)
			if err != nil || string(body) != "public-root\nlab-root\n" {
				t.Fatalf("combined trust=%q: %v", body, err)
			}
		})
	}
}
