// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// ValidateToken makes a single GET /api/v4/projects/{enc-path} call
// with the given token (sent as PRIVATE-TOKEN). Format checks
// (glpat_*) are caller-side. Returns nil on 2xx, an error otherwise.
func (p *Provider) ValidateToken(ctx context.Context, token, repo string) error {
	if token == "" {
		return fmt.Errorf("token is empty: %w", errs.ErrPermissionDenied)
	}

	if repo == "" {
		return fmt.Errorf("repo is empty: %w", errs.ErrUsage)
	}

	if _, err := p.ParseRepository(repo); err != nil {
		return err
	}

	apiBase, _ := p.apiContext()
	endpoint := projectEndpoint(apiBase, repo)

	_, err := getJSON(ctx, p.HTTPClient, endpoint, map[string]string{
		headerPrivateToken: token,
	})
	if err != nil {
		return fmt.Errorf("gitlab token validation: %w", err)
	}

	return nil
}

// ValidateBotPermissions probes the GitLab API with the configured bot
// token. Returns ok=true/false per probe; the use case decides
// severity.
//
// Mapping to GitHub's three probes:
//
//	UserAccessible     → GET /api/v4/user
//	RepoAccessible     → GET /api/v4/projects/{enc-path}
//	BranchesAccessible → GET /api/v4/projects/{enc-path}/repository/branches
//
// Probes fan out so the total cost is one round trip's worth rather
// than three serialised RTTs. Mirrors the github provider's
// ValidateBotPermissions shape so the port stays uniform across
// implementations.
func (p *Provider) ValidateBotPermissions(ctx context.Context, repo string) (*provider.BotPermissions, error) {
	if repo == "" {
		return nil, fmt.Errorf("repo is empty: %w", errs.ErrUsage)
	}

	if _, err := p.ParseRepository(repo); err != nil {
		return nil, err
	}

	token := strings.TrimSpace(p.envFunc()("GITLAB_TOKEN"))
	if token == "" {
		return nil, fmt.Errorf("GitLab user-level bot permission checks require GITLAB_TOKEN; CI_JOB_TOKEN cannot access the user API: %w", errs.ErrPermissionDenied)
	}

	apiBase, _ := p.apiContext()
	headers := map[string]string{headerPrivateToken: token}
	encoded := url.PathEscape(repo)
	probe := func(path string) func() error {
		return func() error {
			_, err := getJSON(ctx, p.HTTPClient, strings.TrimRight(apiBase, "/")+path, headers)

			return err
		}
	}

	push := provider.PushUnknown
	projectProbe := func() error {
		body, err := getJSON(ctx, p.HTTPClient, strings.TrimRight(apiBase, "/")+"/api/v4/projects/"+encoded, headers)
		if err == nil {
			push = projectPushAccess(body)
		}

		return err
	}

	bp, err := provider.ProbeBotPermissions(
		probe("/api/v4/user"),
		projectProbe,
		probe("/api/v4/projects/"+encoded+"/repository/branches"),
	)
	if err != nil {
		return nil, err
	}

	bp.Push = push

	return bp, nil
}

// developerAccessLevel is the lowest GitLab role that may push.
const developerAccessLevel = 30

// projectPushAccess reads the bot's role from a project response. It never
// reports a refusal: the response shows direct project and group membership
// only, so a bot that holds its role through an ancestor group looks like a
// stranger.
func projectPushAccess(body []byte) provider.PushAccess {
	type access struct {
		AccessLevel int `json:"access_level"`
	}

	var project struct {
		Permissions struct {
			ProjectAccess *access `json:"project_access"`
			GroupAccess   *access `json:"group_access"`
		} `json:"permissions"`
	}

	if json.Unmarshal(body, &project) != nil {
		return provider.PushUnknown
	}

	for _, granted := range []*access{project.Permissions.ProjectAccess, project.Permissions.GroupAccess} {
		if granted != nil && granted.AccessLevel >= developerAccessLevel {
			return provider.PushAllowed
		}
	}

	return provider.PushUnknown
}
