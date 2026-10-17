// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Private and job tokens publish to distinct fresh projects, so a package
// uploaded by the first case cannot satisfy the second case's read-back.
func TestGitLabBootstrap_PublishesAndDownloadsTheRecordedBinary(t *testing.T) {
	if !livetest.Selected(t, provider.ForgeGitLab) {
		livetest.NotCoveredf(t, "the contract selects no GitLab endpoint for binary bootstrap")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)
	root := reporoot.Path(t)

	t.Run("private-token", func(t *testing.T) {
		repo := livetest.NewScratchRepo(t, target, "bootstrap-private")
		publishGitLabBootstrap(t, target, repo)
	})

	t.Run("job-token", func(t *testing.T) {
		if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
			t.Skip("native job-token publication requires a deployed runner")
		}

		repo := livetest.NewScratchRepo(t, target, "bootstrap-job")

		const tag = "v0.0.1-bootstrap"

		livetest.PrepareTag(t, target, repo, tag)
		livetest.PublishLayerBinaryAsset(t, target, repo, tag, t.TempDir())
		asset := livetest.ReleaseAssetURL(t, target, repo, tag, "forge-ci")

		var script strings.Builder

		script.WriteString("set -eu\nmkdir -p layer/scripts/ci layer/.forgejo/bin\n")

		for _, path := range []string{"scripts/ci/forgejo-binary.sh", ".forgejo/bin/lib-forge-ci.sh", ".forgejo/bin/forge-ci-linux-amd64.sha256"} {
			body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
			if err != nil {
				t.Fatal(err)
			}

			script.WriteString("printf '%s' '" + base64.StdEncoding.EncodeToString(body) + "' | base64 -d > layer/" + path + "\n")
		}

		script.WriteString("curl --fail --silent --show-error --cacert \"${CI_SERVER_TLS_CA_FILE:?}\" --output engine '" + asset + "'\n")
		script.WriteString(`export FORGE_CI_PACKAGE_PROVIDER=gitlab
export FORGE_CI_REPOSITORY_URL="${CI_SERVER_URL}/${CI_PROJECT_PATH}.git"
export CURL_CA_BUNDLE="${CI_SERVER_TLS_CA_FILE:?}"
export FORGE_CI_PACKAGE_TOKEN_TYPE=job
export FORGE_CI_PACKAGE_TOKEN="${CI_JOB_TOKEN:?}"
bash layer/scripts/ci/forgejo-binary.sh publish amd64 engine
unset FORGE_CI_PACKAGE_TOKEN CI_JOB_TOKEN
source layer/.forgejo/bin/lib-forge-ci.sh
fetch_forge_ci
SIGNER_STRICT=1 verify_forge_ci
"${HELPERS_ENGINE_VERIFIED}" --version
`)
		workflow := "bootstrap:\n  variables:\n    GIT_STRATEGY: empty\n  script:\n    - |\n      " + indent(script.String(), 6)

		if conclusion := livetest.RunWorkflow(t, target, repo, "bootstrap", workflow); conclusion != "success" {
			t.Fatalf("native job-token bootstrap concluded %q", conclusion)
		}
	})
}

func publishGitLabBootstrap(t *testing.T, target livetest.Target, repo string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(livetest.Context(t), 3*time.Minute)
	defer cancel()

	command := exec.CommandContext(ctx, "bash", filepath.Join(reporoot.Path(t), "scripts", "ci", "forgejo-binary.sh"), "publish", "amd64", livetest.LayerBinary(t)) //nolint:gosec // Reviewed repository script and launcher-supplied build, with target-scoped credentials and transport.
	command.Env = livetest.ToolEnvironment(t, target, map[string]string{
		"FORGE_CI_PACKAGE_PROVIDER": "gitlab", "CI_SERVER_URL": target.BaseURL(),
		"FORGE_CI_REPOSITORY_URL": target.BaseURL() + "/" + target.Owner + "/" + repo + ".git",
		"FORGE_CI_PACKAGE_TOKEN":  target.Token, "FORGE_CI_PACKAGE_TOKEN_TYPE": "private",
	})

	output, err := command.CombinedOutput()
	if strings.Contains(string(output), target.Token) {
		t.Fatal("the bootstrap publisher exposed the target credential")
	}

	if err != nil || !strings.Contains(string(output), "Published and verified, readable without credentials") {
		t.Fatalf("private-token publication: %v\n%s", err, output)
	}
}
