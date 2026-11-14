// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"context"
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// pullRefPattern extracts the PR number from a refs/pull/<n>/{head,merge} ref.
var pullRefPattern = regexp.MustCompile(`^refs/pull/(\d+)/`)

// ResolveContext reads the Forgejo Actions env vars and returns a typed
// EventContext. No network, no I/O. Forgejo's act_runner mirrors the
// runner context into both FORGEJO_* and GITHUB_* variables; we prefer
// the FORGEJO_* names and fall back to the GITHUB_* aliases.
func (p *Provider) ResolveContext(_ context.Context) (*provider.EventContext, error) {
	base := p.envFunc()
	// get reads FORGEJO_<suffix>, falling back to GITHUB_<suffix>.
	get := func(suffix string) string {
		return firstNonEmpty(base, "FORGEJO_"+suffix, "GITHUB_"+suffix)
	}

	sha := get("SHA")
	refName := get("REF_NAME")
	refType := classifyRefType(get)

	// The head branch exists only on pull-request events; a push names its
	// branch in the ref. Reading HEAD_REF for every event let a stray or
	// aliased value replace the pushed branch.
	branch := ""

	switch refType {
	case provider.RefTypePR:
		branch = get("HEAD_REF")
	case provider.RefTypeBranch:
		branch = refName
	case provider.RefTypeTag, provider.RefTypeOther:
		// A tag or any other ref names no branch.
	}

	prNumber := ""

	if refType == provider.RefTypePR {
		if m := pullRefPattern.FindStringSubmatch(get("REF")); len(m) == 2 {
			prNumber = m[1]
		}
	}

	repo := get("REPOSITORY")

	// No default host: Forgejo is self-hosted, so an unresolved server yields
	// an empty RepoURL rather than a guess at a specific instance.
	server := get("SERVER_URL")

	repoURL := ""
	if repo != "" && server != "" {
		repoURL = strings.TrimRight(server, "/") + "/" + repo
	}

	return &provider.EventContext{
		ForgeAPI:  provider.ForgeForgejo,
		RefName:   refName,
		RefType:   refType,
		SHA:       sha,
		ShortSHA:  provider.ShortSHA(sha),
		Branch:    branch,
		PRNumber:  prNumber,
		EventName: get("EVENT_NAME"),
		Repo:      repo,
		RepoURL:   repoURL,
	}, nil
}

// classifyRefType maps the runner's EVENT_NAME / REF_TYPE / REF (passed
// via the suffix-aware get) to a provider.RefType.
func classifyRefType(get func(string) string) provider.RefType {
	return provider.ClassifyRef(get("EVENT_NAME"), get("REF_TYPE"), get("REF"))
}
