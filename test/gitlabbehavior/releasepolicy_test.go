// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestReleasePolicyComesFromTrustedInputNotAmbientVariables(t *testing.T) {
	// Run its trusted-input binding before the network/bootstrap portion.
	prefix, _, found := strings.Cut(releaseJobStart(t, "Check: release request", "buildah"), "expected=")
	if !found {
		t.Fatal("cannot locate engine bootstrap boundary")
	}

	for _, policy := range []string{"stable", "stable-prerelease"} {
		t.Run(policy, func(t *testing.T) {
			binding := gitlabrender.Script(t, "templates/release-jobs.yml", prefix, map[string]any{"release-policy": policy, "job-user": ""})
			program := binding + `source "$1"
rc_engine() { printf 'selected=%s\n' "$RELEASE_POLICY"; }
rc_release_context
`
			cmd := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-c", program, "release-policy", filepath.Join(reporoot.Path(t), ".gitlab/bin/release.sh"))
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + cmd.Dir, "CI_DEFAULT_BRANCH=main", "CI_COMMIT_TAG=release-request/v1.0.0", "CI_COMMIT_SHA=" + strings.Repeat("a", 40), "RC_RELEASE_POLICY=stable-prerelease", "RELEASE_POLICY=stable-prerelease",
				"GIT_STRATEGY=empty", "GIT_SUBMODULE_STRATEGY=none", "CI_SERVER_URL=https://gitlab.invalid", "CI_REGISTRY=registry.gitlab.invalid"}

			output, err := cmd.CombinedOutput()
			// The job start says which user it runs as, then the policy.
			_, selected, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
			if err != nil || selected != "selected="+policy {
				t.Fatalf("trusted policy was shadowed: %v\n%s", err, output)
			}
		})
	}
}

func TestCentralPublisherBindsItsOwnReleasePolicy(t *testing.T) {
	scripts := publisherScripts(t, "publish-maven-central.yml")

	prefix, _, found := strings.Cut(scripts[0], "CI_OUTPUT=")
	if !found || !strings.Contains(prefix, "$[[ inputs.release-policy ]]") {
		t.Fatal("Central component has no trusted release-policy input binding")
	}

	for _, policy := range []string{"stable", "stable-prerelease"} {
		t.Run(policy, func(t *testing.T) {
			program := strings.ReplaceAll(prefix, "$[[ inputs.release-policy ]]", policy) + `printf 'selected=%s\n' "$RELEASE_POLICY"`
			cmd := exec.CommandContext(t.Context(), componentBash(t), "--noprofile", "--norc", "-c", program)
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + cmd.Dir, "RELEASE_POLICY=stable-prerelease"}

			output, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(output)) != "selected="+policy {
				t.Fatalf("Central policy not honored: %v\n%s", err, output)
			}
		})
	}
}
