// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// The demo commands, `just demo-list` and `just demo-delete`, find and remove
// what `just demo` made. A demo is found by its name, demo-<name>, and its
// helpers' suffixes; it is deleted repository by repository, each one only
// after the forge confirms it still has the identity the listing showed.

// DemoRepository is one repository of a demo, as the forge lists it. Packages
// are those the forge keeps apart from the repository and binds to it; they
// are listed only where deleting the repository would leave them behind.
type DemoRepository struct {
	Name     string
	ID       int64
	WebURL   string
	Packages []DemoPackage
}

// DemoPackage is one package bound to a demo's repository, as the forge lists
// it.
type DemoPackage struct {
	Type string
	Name string
	// Version is set where the forge lists a package version by version.
	Version string
	ID      int64
}

// DemoSet is one demo and every repository that belongs to it, the demo's
// own repository first.
type DemoSet struct {
	Name         string
	Repositories []DemoRepository
}

// demoOf returns the demo a repository belongs to, from its name: demo-<name>,
// demo-<name>-forge-ci, or demo-<name>-release-<role>.
func demoOf(repo string) (string, bool) {
	name, found := strings.CutPrefix(repo, demoRepoPrefix)
	if !found {
		return "", false
	}

	if before, _, helper := strings.Cut(name, demoHelperInfix); helper {
		name = before
	} else {
		name = strings.TrimSuffix(name, demoLayerSuffix)
	}

	return name, validDemoName(name)
}

// GroupDemos sorts repositories into demos, by name, each demo's own
// repository first and its helpers after it in name order. Repositories that
// are no demo's are left out.
func GroupDemos(repositories []DemoRepository) []DemoSet {
	byName := map[string]*DemoSet{}

	for _, repository := range repositories {
		name, found := demoOf(repository.Name)
		if !found {
			continue
		}

		set, known := byName[name]
		if !known {
			set = &DemoSet{Name: name}
			byName[name] = set
		}

		set.Repositories = append(set.Repositories, repository)
	}

	sets := make([]DemoSet, 0, len(byName))

	for _, set := range byName {
		own := DemoRepo(set.Name)
		slices.SortFunc(set.Repositories, func(first, second DemoRepository) int {
			switch {
			case first.Name == own:
				return -1
			case second.Name == own:
				return 1
			}

			return strings.Compare(first.Name, second.Name)
		})
		sets = append(sets, *set)
	}

	slices.SortFunc(sets, func(first, second DemoSet) int { return strings.Compare(first.Name, second.Name) })

	return sets
}

// AcceptDemoTarget reads the demo account's contract and accepts its endpoint
// for one lab forge, as Accept does for a scenario.
func AcceptDemoTarget(forge provider.ForgeAPI, now time.Time) (Target, error) {
	scope, err := liveProviderScope()
	if err != nil {
		return Target{}, err
	}

	if scope != "" && scope != forge {
		return Target{}, fmt.Errorf("%s excludes %s: %w", liveProviderEnv, forge, errs.ErrValidation)
	}

	lab, err := labContractFor(demoAccount)
	if err != nil {
		return Target{}, err
	}

	target, err := acceptTarget(lab, forge, now)
	if err != nil {
		return Target{}, err
	}

	if target.Owner != demoAccount {
		return Target{}, fmt.Errorf("the %q contract acts as %q: %w", demoAccount, target.Owner, errs.ErrValidation)
	}

	return captureTargetCA(target)
}

// ListLabDemos lists the demos of the demo account on one lab forge.
func ListLabDemos(ctx context.Context, target Target) ([]DemoSet, error) {
	repositories, err := listOwnRepositories(ctx, target)
	if err != nil {
		return nil, err
	}

	// Forgejo keeps packages with the owner; GitLab keeps them in the project
	// and deletes them with it.
	if target.Forge == provider.ForgeForgejo {
		packages, err := ownerPackages(ctx, target)
		if err != nil {
			return nil, err
		}

		for index := range repositories {
			repositories[index].Packages = linkedPackages(packages, repositories[index].ID)
		}
	}

	return GroupDemos(repositories), nil
}

// deleteWithPackages deletes each repository after its packages. A package
// that could not be deleted leaves its repository, and the rest, in place:
// the repository is what still says whose the package is.
func deleteWithPackages(ctx context.Context, repositories []DemoRepository, packages, repository func(context.Context, DemoRepository) error) error {
	for _, listed := range repositories {
		if err := packages(ctx, listed); err != nil {
			return err
		}

		if err := repository(ctx, listed); err != nil {
			return err
		}
	}

	return nil
}

// linkedPackages returns the package versions the forge links to the
// repository of that ID. The link is the forge's own record of whose a
// package is; a package linked to no repository is no demo's, whatever its
// name.
func linkedPackages(packages []OwnerPackage, repositoryID int64) []DemoPackage {
	var linked []DemoPackage

	for _, pkg := range packages {
		if repositoryID > 0 && pkg.RepositoryID == repositoryID {
			linked = append(linked, DemoPackage{Type: pkg.Type, Name: pkg.Name, Version: pkg.Version, ID: pkg.ID})
		}
	}

	return linked
}

// deleteLinkedPackages deletes the package versions listed with a repository,
// before the repository goes and takes the link with it. Each is deleted only
// while the forge still lists that ID as that version linked to that
// repository; one already gone is no error. A second listing must show none
// of them.
func deleteLinkedPackages(ctx context.Context, repository DemoRepository,
	list func(context.Context) ([]OwnerPackage, error), remove func(context.Context, DemoPackage) error,
) error {
	if len(repository.Packages) == 0 {
		return nil
	}

	current, err := list(ctx)
	if err != nil {
		return err
	}

	byID := map[int64]OwnerPackage{}
	for _, pkg := range current {
		byID[pkg.ID] = pkg
	}

	for _, listed := range repository.Packages {
		now, found := byID[listed.ID]
		if !found {
			continue
		}

		if now.Type != listed.Type || now.Name != listed.Name || now.Version != listed.Version || now.RepositoryID != repository.ID {
			return fmt.Errorf("%s package %s %s of %s: %w", listed.Type, listed.Name, listed.Version, repository.Name, errPackageIdentityDrift)
		}

		if err := remove(ctx, listed); err != nil {
			return fmt.Errorf("delete %s package %s %s of %s: %w", listed.Type, listed.Name, listed.Version, repository.Name, err)
		}
	}

	left, err := list(ctx)
	if err != nil {
		return err
	}

	for _, pkg := range left {
		for _, listed := range repository.Packages {
			if pkg.ID == listed.ID {
				return fmt.Errorf("%s package %s %s of %s: %w", listed.Type, listed.Name, listed.Version, repository.Name, errPackageDeletionUnconfirmed)
			}
		}
	}

	return nil
}

// DeleteLabDemo deletes every repository of a demo through the lab client's
// own deletion, which on Forgejo cancels the runs first and on GitLab empties
// the registry and finishes GitLab's deferred deletion. Each repository is
// deleted only while the forge still gives its name the listed ID. On Forgejo
// the package versions the forge links to a repository go before it: the
// owner keeps them otherwise, and the next demo of that name meets them. When the
// account holds no demo afterwards, the Forgejo layer binary package the demos
// shared is deleted too.
func DeleteLabDemo(ctx context.Context, target Target, set DemoSet) error {
	if target.Owner != demoAccount {
		return fmt.Errorf("demos are deleted only in lab account %q: %w", demoAccount, errs.ErrValidation)
	}

	client, err := scratchClient(target)
	if err != nil {
		return err
	}

	repos, err := client.Repositories(demoRepoPrefix)
	if err != nil {
		return err
	}

	lookup := func(ctx context.Context, name string) (int64, bool, error) { return repositoryID(ctx, target, name) }

	list := func(ctx context.Context) ([]OwnerPackage, error) { return ownerPackages(ctx, target) }
	remove := func(ctx context.Context, pkg DemoPackage) error {
		endpoint := target.BaseURL() + "/api/v1/packages/" + url.PathEscape(target.Owner) + "/" + url.PathEscape(pkg.Type) +
			"/" + url.PathEscape(pkg.Name) + "/" + url.PathEscape(pkg.Version)

		return discard(ctx, target, http.MethodDelete, endpoint, nil, http.StatusNoContent, http.StatusNotFound)
	}

	err = deleteWithPackages(ctx, set.Repositories,
		func(ctx context.Context, repository DemoRepository) error {
			return deleteLinkedPackages(ctx, repository, list, remove)
		},
		func(ctx context.Context, repository DemoRepository) error {
			return deleteListed(ctx, target.Owner, repository, lookup, repos.Delete)
		})
	if err != nil {
		return err
	}

	if target.Forge != provider.ForgeForgejo {
		return nil
	}

	left, err := ListLabDemos(ctx, target)
	if err != nil || len(left) != 0 {
		return err
	}

	return deleteLayerPackages(ctx, target)
}

// deleteListed deletes one listed repository while the forge still gives its
// name the listed ID: lookup reads the ID, remove deletes by name. One already
// gone is no error.
func deleteListed(ctx context.Context, owner string, repository DemoRepository,
	lookup func(context.Context, string) (int64, bool, error), remove func(context.Context, string) error,
) error {
	current, found, err := lookup(ctx, repository.Name)
	if err != nil || !found {
		return err
	}

	if current != repository.ID {
		return fmt.Errorf("%s/%s is now repository %d, not the listed %d; list again: %w",
			owner, repository.Name, current, repository.ID, errs.ErrValidation)
	}

	if deleteErr := remove(ctx, repository.Name); deleteErr != nil {
		return fmt.Errorf("delete %s/%s: %w", owner, repository.Name, deleteErr)
	}

	return nil
}

// listOwnRepositories lists every repository the demo account owns.
func listOwnRepositories(ctx context.Context, target Target) ([]DemoRepository, error) {
	var all []DemoRepository

	for page := 1; ; page++ {
		var listed []struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			Path      string `json:"path"`
			HTMLURL   string `json:"html_url"`
			WebURL    string `json:"web_url"`
			Namespace struct {
				FullPath string `json:"full_path"`
			} `json:"namespace"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		}

		endpoint := target.BaseURL() + "/api/v1/user/repos?limit=50&page=" + strconv.Itoa(page)
		if target.Forge == provider.ForgeGitLab {
			endpoint = target.BaseURL() + "/api/v4/projects?owned=true&simple=true&per_page=100&page=" + strconv.Itoa(page)
		}

		if _, err := decode(ctx, target, http.MethodGet, endpoint, nil, &listed, http.StatusOK); err != nil {
			return nil, fmt.Errorf("list the repositories of %s: %w", target.Owner, err)
		}

		for _, item := range listed {
			repository := DemoRepository{Name: item.Name, ID: item.ID, WebURL: item.HTMLURL}
			if target.Forge == provider.ForgeGitLab {
				if item.Namespace.FullPath != target.Owner {
					continue
				}

				repository = DemoRepository{Name: item.Path, ID: item.ID, WebURL: item.WebURL}
			} else if item.Owner.Login != target.Owner {
				continue
			}

			all = append(all, repository)
		}

		if len(listed) == 0 {
			return all, nil
		}
	}
}

// repositoryID returns the ID the forge gives the owner's repository of that
// name, and whether there is one.
func repositoryID(ctx context.Context, target Target, repo string) (int64, bool, error) {
	endpoint := actionsURL(target, repo, "")
	if target.Forge == provider.ForgeGitLab {
		endpoint = target.BaseURL() + "/api/v4/projects/" + url.PathEscape(RepoSlug(target, repo))
	}

	var found struct {
		ID int64 `json:"id"`
	}

	status, err := decode(ctx, target, http.MethodGet, endpoint, nil, &found, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return 0, false, err
	}

	return found.ID, status == http.StatusOK, nil
}

// deleteLayerPackages deletes every version of the layer binary package the
// demo account's layers published, once no demo is left to download one.
func deleteLayerPackages(ctx context.Context, target Target) error {
	owner := url.PathEscape(target.Owner)

	var versions []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	listing := target.BaseURL() + "/api/v1/packages/" + owner + "?type=generic&q=" + url.QueryEscape(layerPackage)
	if _, err := decode(ctx, target, http.MethodGet, listing, nil, &versions, http.StatusOK); err != nil {
		return fmt.Errorf("list the layer packages of %s: %w", target.Owner, err)
	}

	for _, version := range versions {
		if version.Name != layerPackage {
			continue
		}

		endpoint := target.BaseURL() + "/api/v1/packages/" + owner + "/generic/" + url.PathEscape(layerPackage) + "/" + url.PathEscape(version.Version)
		if err := discard(ctx, target, http.MethodDelete, endpoint, nil, http.StatusNoContent, http.StatusNotFound); err != nil {
			return fmt.Errorf("delete layer package %s %s: %w", layerPackage, version.Version, err)
		}
	}

	return nil
}
