// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"context"
	"fmt"

	"code.gitea.io/sdk/gitea"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// ValidateToken confirms the given token can read the repository with a
// single authenticated GET /api/v1/repos/{owner}/{repo}. Returns nil on
// success, a classified error otherwise.
func (p *Provider) ValidateToken(ctx context.Context, token, repo string) error {
	if token == "" {
		return fmt.Errorf("token is empty: %w", errs.ErrPermissionDenied)
	}

	if repo == "" {
		return fmt.Errorf("repo is empty: %w", errs.ErrUsage)
	}

	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	client, err := p.clientWithToken(ctx, token)
	if err != nil {
		return err
	}

	if _, resp, err := client.GetRepo(owner, name); err != nil {
		return fmt.Errorf("forgejo token validation: %w", classifyErr(resp, err))
	}

	return nil
}

// ValidateBotPermissions probes the Forgejo API with the configured bot
// token. Probes fan out so the total cost is roughly one round trip. The
// three probes mirror the github/gitlab providers so the port stays
// uniform:
//
//	UserAccessible     → GET /api/v1/user
//	RepoAccessible     → GET /api/v1/repos/{owner}/{repo}
//	BranchesAccessible → GET /api/v1/repos/{owner}/{repo}/branches
func (p *Provider) ValidateBotPermissions(ctx context.Context, repo string) (*provider.BotPermissions, error) {
	if repo == "" {
		return nil, fmt.Errorf("repo is empty: %w", errs.ErrUsage)
	}

	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	client, err := p.client(ctx)
	if err != nil {
		return nil, err
	}

	push := provider.PushUnknown

	bp, probeErr := provider.ProbeBotPermissions(
		func() error {
			_, resp, err := client.GetMyUserInfo()

			return classifyErr(resp, err)
		},
		func() error {
			repository, resp, err := client.GetRepo(owner, name)
			if err == nil && repository != nil && repository.Permissions != nil {
				push = provider.PushDenied
				if repository.Permissions.Push {
					push = provider.PushAllowed
				}
			}

			return classifyErr(resp, err)
		},
		func() error {
			_, resp, err := client.ListRepoBranches(owner, name, gitea.ListRepoBranchesOptions{})

			return classifyErr(resp, err)
		},
	)
	if probeErr != nil {
		return nil, probeErr
	}

	bp.Push = push

	return bp, nil
}
