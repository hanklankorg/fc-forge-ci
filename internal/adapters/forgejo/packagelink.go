// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"code.gitea.io/sdk/gitea"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// LinkForgePackage implements provider.ForgePackageLinker: it links one of
// the owner's packages to the repository the run belongs to. Forgejo keeps a
// package under its owner and links none to a repository on its own.
//
// The package is read first, so that a package this repository already has
// is left alone and one that another repository has is never taken from it.
// The read also tells the two meanings of a 404 on the link apart: the
// package is listed by then, so what is missing is the route.
func (p *Provider) LinkForgePackage(ctx context.Context, packageType, name string) error {
	owner, repo, err := p.repoFromEnv()
	if err != nil {
		return err
	}

	client, err := p.client(ctx)
	if err != nil {
		return err
	}

	linked, err := packageRepository(client, owner, packageType, name)
	if err != nil {
		return err
	}

	switch {
	case strings.EqualFold(linked, owner+"/"+repo):
		return nil
	case linked != "":
		return fmt.Errorf("it is linked to %s, which keeps it: %w", linked, errs.ErrValidation)
	}

	resp, err := client.LinkPackage(owner, packageType, name, repo)
	if err == nil {
		return nil
	}

	status := 0
	if resp != nil && resp.Response != nil {
		status = resp.StatusCode
	}

	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("this Forgejo has no API that links a package to a repository: %w", errs.ErrUnsupported)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("the token may publish the package but Forgejo refuses it the link to %s/%s: %w",
			owner, repo, errs.ErrPermissionDenied)
	default:
		return fmt.Errorf("forgejo link %s package %s to %s/%s: %w", packageType, name, owner, repo, classifyErr(resp, err))
	}
}

// packageRepository returns the full name of the repository the owner's
// package is linked to, or "" for a package linked to none. Every version
// of a package carries the package's link, so the first one listed decides.
func packageRepository(client *gitea.Client, owner, packageType, name string) (string, error) {
	for page := 1; ; {
		packages, resp, err := client.ListPackages(owner, gitea.ListPackagesOptions{
			Page: page, PageSize: forgejoPackagePageSize,
			Type: packageType,
			Q:    name,
		})
		if err != nil {
			return "", fmt.Errorf("forgejo list %s packages of %s: %w", packageType, owner, classifyErr(resp, err))
		}

		for _, pkg := range packages {
			if pkg == nil || pkg.Type != packageType || pkg.Name != name {
				continue
			}

			if pkg.Repository == nil {
				return "", nil
			}

			return pkg.Repository.FullName, nil
		}

		if resp == nil || resp.NextPage <= page {
			return "", fmt.Errorf("it is %w among the %s packages of %s: %w", provider.ErrForgePackageNotListed, packageType, owner, errs.ErrMissingInput)
		}

		page = resp.NextPage
	}
}
