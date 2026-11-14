// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	githubapi "github.com/google/go-github/v91/github"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

var (
	errRuntimePackageCoordinates     = errors.New("runtime packages require canonical GHCR coordinates under the engine owner")
	errPackageIdentityInput          = errors.New("package identity requires a canonical name and acknowledged repository ID")
	errPackageRepositoryMismatch     = errors.New("container package source repository identity mismatch; refusing adoption")
	errPackageDeletionUnacknowledged = errors.New("exact package deletion requires an acknowledged organization and package ID")
	errPackageIdentityDrift          = errors.New("container package identity drift; refusing deletion")
	errPackageDeletionUnconfirmed    = errors.New("exact container package deletion not confirmed")
)

// GitHubPackageIdentity is an exact, explicitly acknowledged cleanup resource.
// Capture it only for packages this batch created or the operator acknowledged.
// The JSON names are the field names that retained receipts already carry.
type GitHubPackageIdentity struct {
	Organization string `json:"Organization"`
	Name         string `json:"Name"` // raw package name, including nested path; never URL-escaped
	ID           int64  `json:"ID"`
	RepositoryID int64  `json:"RepositoryID"`
}

// RuntimePackageNames lists exact runtime and cache coordinates without listing
// an organization or granting deletion authority. Prepared engines are retained.
func (engine GitHubEngine) RuntimePackageNames() ([]string, error) {
	prefix := "ghcr.io/" + strings.ToLower(engine.Owner) + "/"
	if !container.ValidRepositoryName(engine.Registry()+"/forge-ci-runtime") || !strings.HasPrefix(engine.Registry()+"/", prefix) || !container.ValidRepositoryName(prefix+strings.ToLower(engine.Repository)) {
		return nil, errRuntimePackageCoordinates
	}

	base := strings.TrimPrefix(engine.Registry()+"/", prefix)

	var names []string
	for _, image := range []string{"forge-ci-runtime-base", "forge-ci-runtime", "forge-ci-runtime-rust-stable", "forge-ci-runtime-go-1.27", "forge-ci-runtime-node-24", "forge-ci-runtime-java-25", "forge-ci-runtime-android-35"} {
		names = append(names, base+image)
	}

	return append(names, strings.ToLower(engine.Repository)+"-buildcache"), nil
}

// ReadContainerPackageIdentity checks the exact source repository binding.
// Missing packages return os.ErrNotExist. Nil repository ownership is refused.
func (trial *GitHubTrial) ReadContainerPackageIdentity(ctx context.Context, name string, repositoryID int64) (GitHubPackageIdentity, error) {
	identity := GitHubPackageIdentity{Organization: trial.org, Name: name, RepositoryID: repositoryID}
	if repositoryID <= 0 || !container.ValidRepositoryName("ghcr.io/"+trial.org+"/"+name) {
		return identity, errPackageIdentityInput
	}

	return trial.readPackageIdentity(ctx, packageTypeContainer, identity)
}

// The package types of the organization's registries the trial reads.
const (
	packageTypeContainer = "container"
	packageTypeNPM       = "npm"
	packageTypeMaven     = "maven"
)

// readPackageIdentity completes identity with the ID of the organization's
// package of that type and name, bound to the identity's repository.
func (trial *GitHubTrial) readPackageIdentity(ctx context.Context, packageType string, identity GitHubPackageIdentity) (GitHubPackageIdentity, error) {
	if identity.RepositoryID <= 0 {
		return identity, errPackageIdentityInput
	}

	// go-github v91 applies url.PathEscape once; pass the raw nested name.
	pkg, response, err := trial.api.Organizations.GetPackage(ctx, trial.org, packageType, identity.Name)
	if response != nil && response.StatusCode == http.StatusNotFound {
		return identity, os.ErrNotExist
	}

	if err != nil {
		return identity, fmt.Errorf("read exact %s package: %w", packageType, err)
	}

	if pkg.GetID() <= 0 || pkg.GetName() != identity.Name || pkg.GetRepository() == nil || pkg.GetRepository().GetID() != identity.RepositoryID {
		return identity, errPackageRepositoryMismatch
	}

	identity.ID = pkg.GetID()

	return identity, nil
}

// DeleteContainerPackage deletes only an explicitly acknowledged identity after
// rereading both package and source repository IDs, then confirms absence.
func (trial *GitHubTrial) DeleteContainerPackage(ctx context.Context, acknowledged GitHubPackageIdentity) error {
	if acknowledged.Organization != trial.org || acknowledged.ID <= 0 {
		return errPackageDeletionUnacknowledged
	}

	current, err := trial.ReadContainerPackageIdentity(ctx, acknowledged.Name, acknowledged.RepositoryID)
	if err != nil {
		return err
	}

	if current != acknowledged {
		return errPackageIdentityDrift
	}

	return trial.deletePackage(ctx, packageTypeContainer, acknowledged.Name)
}

// deletePackage deletes the organization's package of that type and name,
// whose identity the caller has just read, then confirms absence.
func (trial *GitHubTrial) deletePackage(ctx context.Context, packageType, name string) error {
	if _, err := trial.api.Organizations.DeletePackage(ctx, trial.org, packageType, name); err != nil {
		return fmt.Errorf("delete exact %s package: %w", packageType, err)
	}

	_, response, err := trial.api.Organizations.GetPackage(ctx, trial.org, packageType, name)
	if err == nil || response == nil || response.StatusCode != http.StatusNotFound {
		return errPackageDeletionUnconfirmed
	}

	return nil
}

// githubReleaseDeclaration is the artifacts configuration the release
// workflow of the staged consumer at dir selects, nil when it has none.
func githubReleaseDeclaration(dir, workflow string) (*config.Config, error) {
	body, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", filepath.Base(workflow))) //nolint:gosec // the staged consumer's own workflow, named by the trial.
	if err != nil {
		return nil, fmt.Errorf("release workflow: %w", err)
	}

	return githubReleaseConfig(dir, workflow, string(body))
}

// githubReleasedNPMPackages are the npm packages the release workflow of the
// staged consumer at dir publishes to GitHub Packages, as the organization's
// registry names them, without the scope: the npm projects that list
// forge-packages under publish-to in the artifacts configuration the
// workflow selects.
func githubReleasedNPMPackages(dir, workflow string) ([]string, error) {
	declared, err := githubReleaseDeclaration(dir, workflow)
	if err != nil || declared == nil {
		return nil, err
	}

	var names []string

	for _, artifact := range declared.Artifacts {
		if artifact.ProjectType != projecttype.NPM || !slices.Contains(artifact.PublishTo, config.PublishForgePackages) {
			continue
		}

		name, err := npmPackageName(filepath.Join(dir, filepath.FromSlash(artifact.WorkingDirectory)))
		if err != nil {
			return nil, err
		}

		names = append(names, name[strings.LastIndex(name, "/")+1:])
	}

	return names, nil
}

// githubReleasesMavenPackages is whether the release workflow of the staged
// consumer at dir publishes Maven packages to GitHub Packages: a Maven
// library, or the exported publications of a Gradle or Android library, that
// the release plan sends to forge-packages.
func githubReleasesMavenPackages(dir, workflow string) (bool, error) {
	declared, err := githubReleaseDeclaration(dir, workflow)
	if err != nil || declared == nil {
		return false, err
	}

	if err := config.Derive(declared); err != nil {
		return false, err
	}

	return slices.ContainsFunc(pipeline.NewConfigPlan(declared).Artifacts.ForgePackages, func(artifact pipeline.PlannedArtifact) bool {
		return artifact.ProjectType != projecttype.NPM
	}), nil
}

// GitHubReleaseSharesPackages reports whether the release of workflow, a
// fixture workflow of the consumer checkout at root under
// <fixtures>/workflows, publishes a package the organization names without
// the repository: whatever its plan sends to forge-packages, npm or Maven,
// or an image under a declared image-name. The visibilities of one cell
// would both publish that package, and a version is published once, so they
// cannot run side by side. An image under the repository's own name, a
// snapshot image included, is the repository's alone.
//
// The trial stages the artifacts configuration as it is, but for its sign
// block (WithTrialKeySigning), so the checkout's declaration is the one the
// staged release reads.
func GitHubReleaseSharesPackages(root, fixtures, workflow string) (bool, error) {
	body, err := os.ReadFile(filepath.Join(root, fixtures, "workflows", filepath.Base(workflow))) //nolint:gosec // the consumer's own fixture workflow, named by the trial.
	if err != nil {
		return false, fmt.Errorf("release workflow: %w", err)
	}

	declared, err := githubReleaseConfig(root, workflow, string(body))
	if err != nil || declared == nil {
		return false, err
	}

	if slices.ContainsFunc(declared.Containers, func(released config.Container) bool { return released.ImageName != "" }) {
		return true, nil
	}

	if err := config.Derive(declared); err != nil {
		return false, err
	}

	return len(pipeline.NewConfigPlan(declared).Artifacts.ForgePackages) > 0, nil
}

// mavenPackagesOf are the organization's Maven packages bound to the
// repository. The registry names a package after coordinates only the build
// knows, so the binding is what tells the trial's packages from another's.
func (trial *GitHubTrial) mavenPackagesOf(ctx context.Context, repositoryID int64) ([]string, error) {
	if repositoryID <= 0 {
		return nil, errPackageIdentityInput
	}

	var names []string

	options := &githubapi.PackageListOptions{PackageType: new(packageTypeMaven), PerPage: 100}

	for {
		packages, response, err := trial.api.Organizations.ListPackages(ctx, trial.org, options)
		if err != nil {
			return nil, fmt.Errorf("list the organization's Maven packages: %w", err)
		}

		for _, pkg := range packages {
			if pkg.GetRepository() != nil && pkg.GetRepository().GetID() == repositoryID {
				names = append(names, pkg.GetName())
			}
		}

		if response.NextPage == 0 {
			return names, nil
		}

		options.Page = response.NextPage
	}
}

// removeMavenPackagesAfter removes, when the test ends, every Maven package
// of the organization that is bound to repo, each reread by name with that
// binding before it is deleted. Unlike an npm package, none is looked for
// beforehand: its name is not known before the build. A package an earlier
// run left under the same coordinates makes the release's publication fail,
// and the readback accepts only a package of this repository. Where the
// readback found one, a cleanup that removes none fails the trial: the
// listing has lost a package that is still there.
func (trial *GitHubTrial) removeMavenPackagesAfter(repo string, repositoryID int64) {
	trial.tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(trial.tb.Context()), 2*time.Minute)
		defer cancel()

		names, err := trial.mavenPackagesOf(ctx, repositoryID)
		if err != nil {
			trial.tb.Errorf("livetest: the Maven packages of %s are not removed: %v", repo, err)

			return
		}

		removed := 0

		for _, name := range names {
			identity, err := trial.readPackageIdentity(ctx, packageTypeMaven, GitHubPackageIdentity{Organization: trial.org, Name: name, RepositoryID: repositoryID})
			if err == nil {
				err = trial.deletePackage(ctx, packageTypeMaven, name)
			}

			if err != nil {
				trial.tb.Errorf("livetest: Maven package %s of %s is not removed: %v", name, repo, err)

				continue
			}

			removed++

			trial.tb.Logf("Maven package deletion confirmed: package=%s id=%d acknowledged-repository=%d", identity.Name, identity.ID, repositoryID)
		}

		if trial.mavenReadBack && removed == 0 {
			trial.tb.Errorf("livetest: the release of %s published a Maven package that was read back, and the cleanup removed none; delete the package it left", repo)
		}
	})
}

// checkReleasedMavenPackages requires the organization's registry to hold a
// Maven package of the repository, and each one it holds to list version.
func (trial *GitHubTrial) checkReleasedMavenPackages(ctx context.Context, repo string, repositoryID int64, final, version string) {
	trial.tb.Helper()

	names, err := trial.mavenPackagesOf(ctx, repositoryID)
	if err != nil {
		trial.tb.Errorf("the release %s publishes Maven packages: %v", final, err)

		return
	}

	if len(names) == 0 {
		trial.tb.Errorf("the release %s publishes Maven packages, and the organization's registry holds none of %s", final, repo)
	}

	for _, name := range names {
		versions, _, err := trial.api.Organizations.PackageGetAllVersions(ctx, trial.org, packageTypeMaven, name, &githubapi.PackageListOptions{PerPage: 100})
		if err != nil {
			trial.tb.Errorf("read the versions of Maven package %s: %v", name, err)

			continue
		}

		listed := make([]string, 0, len(versions))
		for _, published := range versions {
			listed = append(listed, published.GetName())
		}

		if !slices.Contains(listed, version) {
			trial.tb.Errorf("the release %s succeeded, but the organization's registry lists %v of Maven package %s and not %s", final, listed, name, version)

			continue
		}

		trial.mavenReadBack = true

		trial.tb.Logf("Maven package %s:%s is in the organization's registry, a package of %s", name, version, repo)
	}
}

// RemoveReleasedPackagesAfter prepares for the packages the release of
// workflow publishes to the organization's registries: the images of its
// containers and its npm and Maven packages. The organization holds a
// package and not the repository, so each is removed when the test ends,
// whether or not the run got that far, and only as a package of repo. An npm
// package an earlier run left would refuse this release's, so none may exist
// now.
func (trial *GitHubTrial) RemoveReleasedPackagesAfter(repo string, tree GitHubConsumerTree, workflow string) {
	trial.tb.Helper()

	declared, err := githubReleaseDeclaration(tree.Dir, workflow)
	if err != nil {
		trial.tb.Fatalf("livetest: released packages: %v", err)
	}

	names, err := githubReleasedNPMPackages(tree.Dir, workflow)
	if err != nil {
		trial.tb.Fatalf("livetest: released packages: %v", err)
	}

	maven, err := githubReleasesMavenPackages(tree.Dir, workflow)
	if err != nil {
		trial.tb.Fatalf("livetest: released packages: %v", err)
	}

	if declared == nil || (len(names)+len(declared.Containers) == 0 && !maven) {
		return
	}

	repositoryID := trial.RepositoryID(repo)

	if maven {
		trial.removeMavenPackagesAfter(repo, repositoryID)
	}

	// The released images: each container's package under the repository's
	// name, or the image-name it declares under the organization.
	for _, released := range declared.Containers {
		name := strings.ToLower(repo) + "/" + released.Name
		if released.ImageName != "" {
			name = strings.TrimPrefix(released.ImageName, strings.ToLower(trial.org)+"/")
		}

		trial.removeContainerPackageAfter("release image", name, repo, repositoryID)
	}

	for _, name := range names {
		trial.removeNPMPackageAfter(name, repo, repositoryID)
	}
}

// removeNPMPackageAfter requires the organization to hold no npm package of
// the name yet, and removes the one the release publishes when the test
// ends, reread as a package of repo before it is deleted. A release that
// never published it leaves none to remove.
func (trial *GitHubTrial) removeNPMPackageAfter(name, repo string, repositoryID int64) {
	trial.tb.Helper()

	if _, response, err := trial.api.Organizations.GetPackage(trial.tb.Context(), trial.org, packageTypeNPM, name); response == nil || response.StatusCode != http.StatusNotFound {
		trial.tb.Fatalf("livetest: the organization's npm package %s must not exist before the release publishes it (read: %v); delete the one an earlier run left", name, err)
	}

	trial.tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(trial.tb.Context()), 2*time.Minute)
		defer cancel()

		identity, err := trial.readPackageIdentity(ctx, packageTypeNPM, GitHubPackageIdentity{Organization: trial.org, Name: name, RepositoryID: repositoryID})
		if errors.Is(err, os.ErrNotExist) {
			return
		}

		if err == nil {
			err = trial.deletePackage(ctx, packageTypeNPM, name)
		}

		if err != nil {
			trial.tb.Errorf("livetest: npm package %s of %s is not removed: %v", name, repo, err)

			return
		}

		trial.tb.Logf("npm package deletion confirmed: package=%s id=%d acknowledged-repository=%d", identity.Name, identity.ID, repositoryID)
	})
}

// checkReleasedPackages requires each package the release of workflow
// publishes to be in the organization's npm or Maven registry as a package
// of repo, at the version of the final tag. The registry's own API is the
// witness, not npm or Maven.
func (trial *GitHubTrial) checkReleasedPackages(repo string, tree GitHubConsumerTree, workflow, final string) {
	trial.tb.Helper()

	names, err := githubReleasedNPMPackages(tree.Dir, workflow)
	if err != nil {
		trial.tb.Fatalf("livetest: released packages: %v", err)
	}

	maven, err := githubReleasesMavenPackages(tree.Dir, workflow)
	if err != nil {
		trial.tb.Fatalf("livetest: released packages: %v", err)
	}

	if len(names) == 0 && !maven {
		return
	}

	ctx, version := trial.tb.Context(), strings.TrimPrefix(final, "v")
	repositoryID := trial.RepositoryID(repo)

	if maven {
		trial.checkReleasedMavenPackages(ctx, repo, repositoryID, final, version)
	}

	for _, name := range names {
		if _, err := trial.readPackageIdentity(ctx, packageTypeNPM, GitHubPackageIdentity{Organization: trial.org, Name: name, RepositoryID: repositoryID}); err != nil {
			trial.tb.Errorf("the release %s publishes npm package %s, and the organization's registry holds none of %s: %v", final, name, repo, err)

			continue
		}

		versions, _, err := trial.api.Organizations.PackageGetAllVersions(ctx, trial.org, packageTypeNPM, name, &githubapi.PackageListOptions{PerPage: 100})
		if err != nil {
			trial.tb.Errorf("read the versions of npm package %s: %v", name, err)

			continue
		}

		listed := make([]string, 0, len(versions))
		for _, published := range versions {
			listed = append(listed, published.GetName())
		}

		if !slices.Contains(listed, version) {
			trial.tb.Errorf("the release %s succeeded, but the organization's registry lists %v of npm package %s and not %s", final, listed, name, version)

			continue
		}

		trial.tb.Logf("npm package %s@%s is in the organization's registry, a package of %s", name, version, repo)
	}
}
