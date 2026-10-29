// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Inspect only the exact retained engine's runtime coordinates. This grants no
// deletion authority and does not create packages or modify repository grants.
func TestGitHubEngineRuntimePackagePreflight(t *testing.T) {
	if os.Getenv(GitHubContextEnv) == "" {
		t.Skip("admitted hosted SDK context required")
	}
	organizationID, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_ID"), 10, 64)
	if err != nil || organizationID <= 0 {
		t.Fatal("explicit admitted organization ID required")
	}
	repositoryID, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_ENGINE_REPOSITORY_ID"), 10, 64)
	name := os.Getenv("FORGE_CI_LIVE_ENGINE_REPOSITORY")
	if err != nil || repositoryID <= 0 || name == "" {
		t.Fatal("exact retained engine identity required")
	}
	trial := OpenGitHubTrial(t, organizationID)
	if !strings.HasPrefix(name, trial.prefix) {
		t.Fatal("engine repository outside admitted prefix")
	}
	repository, _, err := trial.api.Repositories.Get(t.Context(), trial.org, name)
	if err != nil || repository.GetID() != repositoryID || repository.GetPrivate() {
		t.Fatal("public retained engine identity mismatch")
	}
	engine := GitHubEngine{Owner: trial.org, Repository: name, Namespace: "ghcr.io/" + strings.ToLower(trial.org+"/"+name)}
	names, err := engine.RuntimePackageNames()
	if err != nil || len(names) != 8 {
		t.Fatal("expected exactly seven runtimes and one cache")
	}
	for _, packageName := range names {
		identity, err := trial.ReadContainerPackageIdentity(t.Context(), packageName, repositoryID)
		if errors.Is(err, os.ErrNotExist) {
			t.Logf("exact runtime preflight: absent %s", packageName)
			continue
		}
		if err != nil {
			t.Fatalf("exact runtime preflight refused %s: %v", packageName, err)
		}
		t.Logf("exact runtime preflight: name=%s id=%d source_repository_id=%d", identity.Name, identity.ID, identity.RepositoryID)
	}
}
