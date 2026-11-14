// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"context"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// pullRefPattern extracts the PR number from a refs/pull/<n>/{head,merge} ref.
var pullRefPattern = regexp.MustCompile(`^refs/pull/(\d+)/`)

// ResolveContext reads the canonical GitHub Actions env vars and returns
// a typed EventContext. No network, no I/O.
func (p *Provider) ResolveContext(_ context.Context) (*provider.EventContext, error) {
	get := p.envFunc()

	sha := get("GITHUB_SHA")
	refName := get("GITHUB_REF_NAME")
	refType := classifyRefType(get)

	// The head branch exists only on pull-request events; a push names its
	// branch in the ref. Reading HEAD_REF for every event let a stray or
	// aliased value replace the pushed branch.
	branch := ""

	switch refType {
	case provider.RefTypePR:
		branch = get("GITHUB_HEAD_REF")
	case provider.RefTypeBranch:
		branch = refName
	case provider.RefTypeTag, provider.RefTypeOther:
		// A tag or any other ref names no branch.
	}

	prNumber := ""

	if refType == provider.RefTypePR {
		if m := pullRefPattern.FindStringSubmatch(get("GITHUB_REF")); len(m) == 2 {
			prNumber = m[1]
		}
	}

	repo := get("GITHUB_REPOSITORY")

	server := get("GITHUB_SERVER_URL")
	if server == "" {
		server = defaultServerURL
	}

	repoURL := ""
	if repo != "" {
		repoURL = strings.TrimRight(server, "/") + "/" + repo
	}

	return &provider.EventContext{
		ForgeAPI:  provider.ForgeGitHub,
		RefName:   refName,
		RefType:   refType,
		SHA:       sha,
		ShortSHA:  provider.ShortSHA(sha),
		Branch:    branch,
		PRNumber:  prNumber,
		EventName: get("GITHUB_EVENT_NAME"),
		Repo:      repo,
		RepoURL:   repoURL,
	}, nil
}

// classifyRefType maps GITHUB_EVENT_NAME / GITHUB_REF_TYPE / GITHUB_REF
// to a provider.RefType, with the same rules as the Forgejo adapter.
func classifyRefType(get func(string) string) provider.RefType {
	return provider.ClassifyRef(get("GITHUB_EVENT_NAME"), get("GITHUB_REF_TYPE"), get("GITHUB_REF"))
}
