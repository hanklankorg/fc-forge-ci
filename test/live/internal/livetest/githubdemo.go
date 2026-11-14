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
	// deleteRepository deletes a repository of the organization by the ID it
	// was listed with: the hosted client's DeleteRepository.
	deleteRepository func(ctx context.Context, name string, id int64) error
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

	return &GitHubDemos{
		client: client, api: client.API(), org: document.Organization.Login, prefix: document.Prefix,
		deleteRepository: client.DeleteRepository,
	}, nil
}

// Close releases the hosted client.
func (demos *GitHubDemos) Close() { demos.client.Close() }

// List returns the organization's demos. A repository's name is given without
// the session prefix, as on the lab, and carries the packages GitHub binds to
// it.
func (demos *GitHubDemos) List(ctx context.Context) ([]DemoSet, error) {
	var repositories []DemoRepository

	options := &github.RepositoryListByOrgOptions{PerPage: 100}

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
			break
		}

		options.Page = response.NextPage
	}

	packages, err := demos.packagesByRepository(ctx)
	if err != nil {
		return nil, err
	}

	for index := range repositories {
		repositories[index].Packages = packages[repositories[index].ID]
	}

	return GroupDemos(repositories), nil
}

// Delete deletes every repository of a demo through the hosted client, which
// deletes by the listed ID after confirming the ID still has that name in the
// organization. GitHub keeps a deleted repository's packages and unbinds
// them, and a new demo of the same name is then refused its own package
// names. So the packages listed with a repository go first, while the
// binding still says whose they are: each is reread, deleted only if it is
// still that package of that repository, and confirmed absent. A package
// bound to no repository is never deleted here; one left by an earlier
// deletion is removed by hand.
func (demos *GitHubDemos) Delete(ctx context.Context, set DemoSet) error {
	for _, repository := range set.Repositories {
		for _, listed := range repository.Packages {
			if err := demos.deletePackage(ctx, repository, listed); err != nil {
				return fmt.Errorf("delete %s package %s of %s/%s: %w", listed.Type, listed.Name, demos.org, demos.prefix+repository.Name, err)
			}
		}

		if err := demos.deleteRepository(ctx, demos.prefix+repository.Name, repository.ID); err != nil {
			return fmt.Errorf("delete %s/%s: %w", demos.org, demos.prefix+repository.Name, err)
		}
	}

	return nil
}

// Org is the organization the demos live in.
func (demos *GitHubDemos) Org() string { return demos.org }

// packagesByRepository lists the organization's container, npm and Maven
// packages by the ID of the repository each is bound to. A package bound to
// no repository is left out: nothing says whose it is, so no demo owns it.
func (demos *GitHubDemos) packagesByRepository(ctx context.Context) (map[int64][]DemoPackage, error) {
	bound := map[int64][]DemoPackage{}

	for _, packageType := range []string{packageTypeContainer, packageTypeNPM, packageTypeMaven} {
		options := &github.PackageListOptions{PackageType: &packageType, PerPage: 100}

		for {
			page, response, err := demos.api.Organizations.ListPackages(ctx, demos.org, options)
			if err != nil {
				return nil, fmt.Errorf("list the %s packages of %s: %w", packageType, demos.org, err)
			}

			for _, pkg := range page {
				if id := pkg.GetRepository().GetID(); id > 0 && pkg.GetID() > 0 {
					bound[id] = append(bound[id], DemoPackage{Type: packageType, Name: pkg.GetName(), ID: pkg.GetID()})
				}
			}

			if response.NextPage == 0 {
				break
			}

			options.Page = response.NextPage
		}
	}

	return bound, nil
}

func (demos *GitHubDemos) deletePackage(ctx context.Context, repository DemoRepository, listed DemoPackage) error {
	current, _, err := demos.api.Organizations.GetPackage(ctx, demos.org, listed.Type, listed.Name)
	if err != nil {
		return fmt.Errorf("read exact %s package: %w", listed.Type, err)
	}

	if current.GetID() != listed.ID || current.GetName() != listed.Name || current.GetRepository().GetID() != repository.ID {
		return errPackageIdentityDrift
	}

	return deleteOrganizationPackage(ctx, demos.api, demos.org, listed.Type, listed.Name)
}
