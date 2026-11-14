// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// ValidateToken makes a single GET /repos/{repo} call with the given
// token. Format / prefix checks happen in the use-case layer; this
// method only reports whether the API accepts the token for that repo.
func (p *Provider) ValidateToken(ctx context.Context, token, repo string) error {
	if _, _, err := splitRepo(repo); err != nil {
		return err
	}

	if token == "" {
		return fmt.Errorf("token is empty: %w", errs.ErrPermissionDenied)
	}

	if repo == "" {
		return fmt.Errorf("repo is empty: %w", errs.ErrUsage)
	}

	_, err := getJSON(ctx, p.HTTPClient, p.restURL("/repos/"+repo), map[string]string{
		"Accept":               acceptJSONHeader,    //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
		"X-GitHub-Api-Version": apiVersionHeader,    //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
		"Authorization":        bearerHeader(token), //nolint:goconst // test fixture / generic identifier — extracting would explode setup boilerplate.
	})
	if err != nil {
		return fmt.Errorf("github token validation: %w", err)
	}

	return nil
}

// ValidateBotPermissions runs three best-effort probes against the
// configured bot token (read from $GITHUB_TOKEN via the env getter).
// Returns ok=true / ok=false per probe rather than failing fast — the
// use case decides which probe is fatal vs warn-only.
//
// The probes are independent HTTP GETs, so they fan out via goroutines
// rather than serialising 3 sequential RTTs. http.Client connection
// pooling (and HTTP/2 multiplexing where the server supports it) keeps
// the concurrent cost ~one round trip's worth, vs ~three sequentially.
func (p *Provider) ValidateBotPermissions(ctx context.Context, repo string) (*provider.BotPermissions, error) {
	if _, _, err := splitRepo(repo); err != nil {
		return nil, err
	}

	get := p.envFunc()
	token := get("GITHUB_TOKEN")

	headers := map[string]string{
		"Accept":               acceptJSONHeader,
		"X-GitHub-Api-Version": apiVersionHeader,
		"Authorization":        bearerHeader(token),
	}
	probe := func(path string) func() error {
		return func() error {
			_, err := getJSON(ctx, p.HTTPClient, p.restURL(path), headers)

			return err
		}
	}

	push := provider.PushUnknown
	repoProbe := func() error {
		body, err := getJSON(ctx, p.HTTPClient, p.restURL("/repos/"+repo), headers)
		if err == nil {
			push = repositoryPushAccess(body)
		}

		return err
	}

	bp, err := provider.ProbeBotPermissions(probe("/user"), repoProbe, probe("/repos/"+repo+"/branches"))
	if err != nil {
		return nil, err
	}

	bp.Push = push

	return bp, nil
}

// repositoryPushAccess reads permissions.push from a repository response.
// A personal token's response carries the user's permissions; a GitHub App
// token's carries none, which is no refusal.
func repositoryPushAccess(body []byte) provider.PushAccess {
	var repository struct {
		Permissions *struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}

	if json.Unmarshal(body, &repository) != nil || repository.Permissions == nil {
		return provider.PushUnknown
	}

	if repository.Permissions.Push {
		return provider.PushAllowed
	}

	return provider.PushDenied
}
