// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"context"
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
)

// BranchHead resolves a branch through the existing bounded, authenticated SDK
// client. The expected source server must match the runner's forge, so a source
// guard cannot accidentally query another forge or forward its credential there.
func (p *Provider) BranchHead(ctx context.Context, serverURL, repository, branch string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	server, err := p.serverURL()
	if err != nil {
		return "", err
	}

	if strings.TrimSuffix(serverURL, "/") != server || !domaingit.ValidRefName("refs/heads/"+branch) {
		return "", fmt.Errorf("branch source must name this forge and a valid branch: %w", errs.ErrValidation)
	}

	owner, name, err := splitRepo(repository)
	if err != nil {
		return "", err
	}

	client, err := p.client(ctx)
	if err != nil {
		return "", err
	}

	result, response, err := client.GetRepoBranch(owner, name, branch)
	if err != nil {
		return "", fmt.Errorf("forgejo branch head: %w", classifyErr(response, err))
	}

	if result == nil || result.Commit == nil || !domaingit.ValidCommitSHA(result.Commit.ID) {
		return "", fmt.Errorf("forgejo branch head is missing a full commit identity: %w", errs.ErrMalformedInput)
	}

	return result.Commit.ID, nil
}
