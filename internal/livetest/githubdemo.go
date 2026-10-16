// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"os"
	"strings"

	"codefloe.com/itiquette/forge-lab-contract/githubclient"
	"codefloe.com/itiquette/forge-lab-contract/githubcontext"
	"github.com/google/go-github/v91/github"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// GitHubDemos lists and deletes the demos in the hosted test organization:
// the repositories <prefix>demo-<name> that `just demo github` made, inside
// Forge Lab's hosted session.
type GitHubDemos struct {
	client *githubclient.Client
	api    *github.Client
	org    string
	prefix string
}

// OpenGitHubDemos loads Forge Lab's hosted context and authorizes exactly the
// organization ID the caller named; Close releases it.
func OpenGitHubDemos(ctx context.Context, organizationID int64) (*GitHubDemos, error) {
	path := os.Getenv(GitHubContextEnv)
	if path == "" {
		return nil, fmt.Errorf("no hosted GitHub context; run inside `just github run` of Forge Lab, which sets %s: %w", GitHubContextEnv, errs.ErrUsage)
	}

	document, err := githubcontext.Load(path)
	if err != nil {
		return nil, fmt.Errorf("hosted GitHub context: %w", err)
	}

	client, err := githubclient.New(ctx, document, organizationID)
	if err != nil {
		return nil, fmt.Errorf("hosted GitHub organization %d: %w", organizationID, err)
	}

	return &GitHubDemos{client: client, api: client.API(), org: document.Organization.Login, prefix: document.Prefix}, nil
}

// Close releases the hosted client.
func (demos *GitHubDemos) Close() { demos.client.Close() }

// List returns the organization's demos. A repository's name is given without
// the session prefix, as on the lab.
func (demos *GitHubDemos) List(ctx context.Context) ([]DemoSet, error) {
	var repositories []DemoRepository

	options := &github.RepositoryListByOrgOptions{ListOptions: github.ListOptions{PerPage: 100}}

	for {
		page, response, err := demos.api.Repositories.ListByOrg(ctx, demos.org, options)
		if err != nil {
			return nil, fmt.Errorf("list the repositories of %s: %w", demos.org, err)
		}

		for _, repository := range page {
			if name, found := strings.CutPrefix(repository.GetName(), demos.prefix); found && strings.HasPrefix(name, demoRepoPrefix) {
				repositories = append(repositories, DemoRepository{Name: name, ID: repository.GetID(), WebURL: repository.GetHTMLURL()})
			}
		}

		if response.NextPage == 0 {
			return GroupDemos(repositories), nil
		}

		options.Page = response.NextPage
	}
}

// Delete deletes every repository of a demo through the hosted client, which
// deletes by the listed ID after confirming the ID still has that name in the
// organization. Container packages outlive a repository on GitHub, and a
// fine-grained token cannot reach the packages API: Delete names them.
func (demos *GitHubDemos) Delete(ctx context.Context, set DemoSet) error {
	for _, repository := range set.Repositories {
		if err := demos.client.DeleteRepository(ctx, demos.prefix+repository.Name, repository.ID); err != nil {
			return fmt.Errorf("delete %s/%s: %w", demos.org, demos.prefix+repository.Name, err)
		}
	}

	return nil
}

// Org is the organization the demos live in.
func (demos *GitHubDemos) Org() string { return demos.org }
