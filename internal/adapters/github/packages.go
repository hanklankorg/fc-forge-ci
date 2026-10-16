// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	gogithub "github.com/google/go-github/v76/github"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const (
	// containerPackageType is GitHub Packages' type for GHCR images.
	containerPackageType = "container"
	// githubTokenEnv is the runner-injected job token.
	githubTokenEnv = "GITHUB_TOKEN" //nolint:gosec // G101: env var name, not a value.
)

// Packages reads and deletes container package versions through the GitHub
// Packages API: the only way to remove anything from GHCR, which does not
// delete tags through the registry API. A version is one manifest; deleting it
// deletes every tag it holds.
//
// The token is the job's GITHUB_TOKEN with packages: write for packages linked
// to the repository, or a classic token with read:packages and delete:packages
// for any package of the owner.
type Packages struct {
	client *gogithub.Client
}

// NewPackages builds the GHCR package client against api.github.com. The registry
// owns this authority: a Forgejo runner's GITHUB_API_URL names its own forge,
// and must never receive the GitHub Packages credential.
func NewPackages(httpClient *http.Client, token string) (*Packages, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errs.CredentialRequired(errs.Credential{What: "GitHub Packages token", Env: githubTokenEnv})
	}

	if httpClient == nil {
		httpClient = defaultHTTPClient()
	}

	client := gogithub.NewClient(httpretry.SameAuthority(httpClient, "GitHub")).WithAuthToken(token)

	return &Packages{client: client}, nil
}

// PackageVersions lists every version of one container package. The owner may
// be an organization or a user; the organization endpoint is asked first.
func (p *Packages) PackageVersions(ctx context.Context, owner, name string) ([]container.PackageVersion, error) {
	var versions []container.PackageVersion

	for page := 1; page != 0; {
		options := &gogithub.PackageListOptions{ListOptions: gogithub.ListOptions{Page: page, PerPage: 100}}

		batch, response, err := p.client.Organizations.PackageGetAllVersions(ctx, owner, containerPackageType, name, options)
		if isNotFound(err) {
			// go-github escapes the package name for organizations but not for
			// users, and a GHCR name may hold slashes.
			batch, response, err = p.client.Users.PackageGetAllVersions(ctx, owner, containerPackageType, url.PathEscape(name), options)
		}

		if err != nil {
			return nil, fmt.Errorf("list versions of package %s/%s: %w", owner, name, classifyPackagesError(err))
		}

		for _, version := range batch {
			converted, err := packageVersion(version)
			if err != nil {
				return nil, fmt.Errorf("package %s/%s: %w", owner, name, err)
			}

			versions = append(versions, converted)
		}

		page = response.NextPage
	}

	return versions, nil
}

// ContainerPackages lists the owner's container package names that start with
// prefix.
func (p *Packages) ContainerPackages(ctx context.Context, owner, prefix string) ([]string, error) {
	var names []string

	for page := 1; page != 0; {
		options := &gogithub.PackageListOptions{
			PackageType: gogithub.Ptr(containerPackageType),
			ListOptions: gogithub.ListOptions{Page: page, PerPage: 100},
		}

		batch, response, err := p.client.Organizations.ListPackages(ctx, owner, options)
		if isNotFound(err) {
			batch, response, err = p.client.Users.ListPackages(ctx, owner, options)
		}

		if err != nil {
			return nil, fmt.Errorf("list container packages of %s: %w", owner, classifyPackagesError(err))
		}

		for _, pkg := range batch {
			if name := pkg.GetName(); strings.HasPrefix(name, prefix) {
				names = append(names, name)
			}
		}

		page = response.NextPage
	}

	slices.Sort(names)

	return names, nil
}

// DeletePackageVersion deletes one version and every tag it holds.
func (p *Packages) DeletePackageVersion(ctx context.Context, owner, name string, id int64) error {
	_, err := p.client.Organizations.PackageDeleteVersion(ctx, owner, containerPackageType, name, id)
	if isNotFound(err) {
		_, err = p.client.Users.PackageDeleteVersion(ctx, owner, containerPackageType, url.PathEscape(name), id)
	}

	if err != nil {
		return fmt.Errorf("delete version %d of package %s/%s: %w", id, owner, name, classifyPackagesError(err))
	}

	return nil
}

func packageVersion(version *gogithub.PackageVersion) (container.PackageVersion, error) {
	converted := container.PackageVersion{
		ID:      version.GetID(),
		Digest:  version.GetName(),
		Created: version.GetCreatedAt().Time,
	}

	if len(version.Metadata) != 0 {
		var metadata gogithub.PackageMetadata
		if err := json.Unmarshal(version.Metadata, &metadata); err != nil {
			return container.PackageVersion{}, fmt.Errorf("version %d metadata: %w: %w", converted.ID, err, errs.ErrMalformedInput)
		}

		if metadata.Container != nil {
			converted.Tags = metadata.Container.Tags
		}
	}

	if !container.ValidDigest(converted.Digest) {
		return container.PackageVersion{}, fmt.Errorf("version %d is named %q, not a manifest digest: %w", converted.ID, converted.Digest, errs.ErrMalformedInput)
	}

	return converted, nil
}

func isNotFound(err error) bool {
	var response *gogithub.ErrorResponse

	return errors.As(err, &response) && response.Response != nil && response.Response.StatusCode == http.StatusNotFound
}

// classifyPackagesError is classifyGitHubError for the package API, where a
// 404 is a missing package or version, not a missing release. A 403 that
// follows a successful listing usually means the token lacks delete:packages.
func classifyPackagesError(err error) error {
	if isNotFound(err) {
		return fmt.Errorf("%w: %w", err, errs.ErrMissingInput)
	}

	return classifyGitHubError(err)
}
