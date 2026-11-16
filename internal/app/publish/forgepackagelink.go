// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainpublish "github.com/diggsweden/forge-ci/internal/domain/publish"
)

// LinkPackages links the packages a run has published to the repository it
// ran in, on a forge that keeps packages under their owner. Any other forge
// does not implement the role and nothing happens.
//
// The packages are published by the time this runs, and a publication is not
// taken back. A link the forge refuses is therefore a warning that names the
// package and the reason, and the publication stays a success: the package
// is complete and installable without the link, and the next publication of
// the package links it.
func LinkPackages(ctx context.Context, forge any, stderr io.Writer, packageType string, names ...string) {
	linkPackages(ctx, forge, stderr, packageType, false, names)
}

// linkPackages is LinkPackages and reports whether the forge listed any of
// the names. With mayBeUnpublished, a name the forge does not list is no
// warning: the publisher, not the engine, decided whether to publish it.
func linkPackages(ctx context.Context, forge any, stderr io.Writer, packageType string, mayBeUnpublished bool, names []string) bool {
	linker, ok := forge.(provider.ForgePackageLinker)
	if !ok {
		return false
	}

	listed := false

	for _, name := range names {
		err := linker.LinkForgePackage(ctx, packageType, name)
		notListed := errors.Is(err, provider.ErrForgePackageNotListed)
		listed = listed || !notListed

		if err == nil || mayBeUnpublished && notListed {
			continue
		}

		_, _ = fmt.Fprintf(stderr, "Warning: %s package %s is published but not linked to the repository: %v\n", packageType, name, err)
	}

	return listed
}

// ForgeMavenDeploy runs mvn deploy to the forge's registry and, on a forge
// that keeps packages under their owner, links the projects it published.
//
// The projects come from one effective POM of the project or reactor in the
// working directory, resolved before anything is published, with the
// deploy's options and, as the deploy replaces the user's settings.xml, with
// none of the user's settings either. Snapshot mode checks every project's
// version from it. Maven decides which projects it deploys, so a project the
// forge does not list was not deployed and is not linked; see LinkPackages
// for a refused link.
func ForgeMavenDeploy(ctx context.Context, model, deploy MavenOps, resolver provider.ForgeMavenRegistryResolver, stdout, stderr io.Writer, in ForgePackagesDeployInput, snapshot bool) error {
	if err := validateMavenModelOptions(in.CLIOpts); err != nil {
		return err
	}

	_, links := resolver.(provider.ForgePackageLinker)

	projects, err := forgeMavenReactor(ctx, model, stderr, in, snapshot, links)
	if err != nil {
		return err
	}

	if err := ForgePackagesDeploy(ctx, deploy, resolver, stdout, stderr, in); err != nil {
		return err
	}

	// A deploy that succeeded published at least one project, so a forge
	// that lists none of them names them differently and links nothing.
	if links && !linkPackages(ctx, resolver, stderr, "maven", true, projects) {
		_, _ = fmt.Fprintf(stderr, "Warning: the Maven packages are published but not linked to the repository: the forge lists none of %s\n", strings.Join(projects, ", "))
	}

	return nil
}

// forgeMavenReactor reads the effective POM only when snapshot mode checks
// its versions or the forge links packages, and returns the projects to
// link: none when it does not link.
func forgeMavenReactor(ctx context.Context, model MavenOps, stderr io.Writer, in ForgePackagesDeployInput, snapshot, links bool) ([]string, error) {
	if !snapshot && !links {
		return nil, nil
	}

	body, err := forgeMavenModel(ctx, model, stderr, in)
	if err != nil {
		return nil, err
	}

	if snapshot {
		if err := domainpublish.ValidateMavenReactorVersions(body, true, ""); err != nil {
			return nil, err
		}
	}

	if !links {
		return nil, nil
	}

	return domainpublish.MavenReactorCoordinates(body)
}

// forgeMavenModel is the effective POM a forge deploy with in resolves: the
// deploy runs with a settings file of its own, so the model runs with an
// empty one rather than the user's.
func forgeMavenModel(ctx context.Context, ops MavenOps, stderr io.Writer, in ForgePackagesDeployInput) ([]byte, error) {
	settings, cleanup, err := writeTempSecretFile("forge-ci-model-settings-*.xml", "<settings/>\n")
	if err != nil {
		return nil, err
	}

	defer cleanup()

	return effectiveMavenModel(ctx, ops, stderr, MavenCentralDeployInput{CLIOpts: in.CLIOpts, SettingsPath: settings, Profile: in.Profile})
}

// LinkMavenPublications links the Maven packages of an uploaded publication
// directory to the run's repository; see LinkPackages for a refused link.
func LinkMavenPublications(ctx context.Context, forge any, stderr io.Writer, directory, version string, snapshot bool) {
	if _, ok := forge.(provider.ForgePackageLinker); !ok {
		return
	}

	set, err := inspectMavenPublication(directory, version, false, snapshot)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Warning: the Maven packages are published but not linked to the repository: %v\n", err)

		return
	}

	names := make([]string, 0, len(set.Publications))
	for _, publication := range set.Publications {
		names = append(names, publication.GroupID+":"+publication.ArtifactID)
	}

	LinkPackages(ctx, forge, stderr, "maven", names...)
}
