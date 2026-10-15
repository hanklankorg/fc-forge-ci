// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package deps

import (
	"os"

	"github.com/diggsweden/forge-ci/internal/adapters/github"
	"github.com/diggsweden/forge-ci/internal/adapters/platform"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// CheckoutProvider binds metadata discovery to the preflighted checkout
// destination. The credential is checked against its ORIGINAL audience before
// overlaying server variables. Explicit file credentials override API auth too,
// including empty files. No process env is changed.
func CheckoutProvider(server string, credential runcontext.Credential, explicitToken bool) (provider.Provider, error) {
	forge := platform.Detect()
	overlay := make(map[string]string)

	for _, key := range runcontext.Token().Keys() {
		overlay[key] = ""
	}

	overlay["GITLAB_TOKEN"], overlay["GH_TOKEN"] = "", ""
	token := credential.For(server)

	switch forge {
	case provider.ForgeGitHub:
		overlay["GITHUB_SERVER_URL"] = server
		overlay["GITHUB_API_URL"] = github.RepositoryAPIBase(server, os.Getenv)
		overlay["GITHUB_TOKEN"] = token
	case provider.ForgeGitLab:
		overlay["CI_SERVER_URL"] = server
		// A deliberately configured API PAT can discover metadata that a GitLab
		// job token cannot read. Explicit file input still overrides it, even empty.
		if apiToken := os.Getenv("GITLAB_TOKEN"); !explicitToken && apiToken != "" {
			overlay["GITLAB_TOKEN"] = apiToken
		} else if credential.GitUsername() == "gitlab-ci-token" {
			overlay["CI_JOB_TOKEN"] = token
		} else {
			overlay["GITLAB_TOKEN"] = token
		}
	case provider.ForgeForgejo:
		for _, key := range runcontext.ServerURL().Keys() {
			overlay[key] = ""
		}

		overlay["FORGEJO_SERVER_URL"], overlay["FORGEJO_TOKEN"] = server, token
	case provider.ForgeLocal:
		// Local metadata is empty and performs no API request.
	}

	return providerForWithEnv(forge, providerEnvironment(overlay))
}
