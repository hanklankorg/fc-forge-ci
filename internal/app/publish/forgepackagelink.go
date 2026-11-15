// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"errors"
	"fmt"
	"io"

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

// linkPackages is LinkPackages. With mayBeUnpublished, a name the forge does
// not list is no warning: the publisher, not the engine, decided whether to
// publish it.
func linkPackages(ctx context.Context, forge any, stderr io.Writer, packageType string, mayBeUnpublished bool, names []string) {
	linker, ok := forge.(provider.ForgePackageLinker)
	if !ok {
		return
	}

	for _, name := range names {
		err := linker.LinkForgePackage(ctx, packageType, name)
		if err == nil || mayBeUnpublished && errors.Is(err, provider.ErrForgePackageNotListed) {
			continue
		}

		_, _ = fmt.Fprintf(stderr, "Warning: %s package %s is published but not linked to the repository: %v\n", packageType, name, err)
	}
}

// LinkMavenProjects links the Maven packages a mvn deploy of the project or
// reactor in the working directory published, read from its effective POM
// with the deployment's options. Maven decides which projects it deploys, so
// a project the forge does not list was not deployed and is not linked; see
// LinkPackages for a refused link.
func LinkMavenProjects(ctx context.Context, ops MavenOps, forge any, stderr io.Writer, in MavenCentralDeployInput) {
	if _, ok := forge.(provider.ForgePackageLinker); !ok {
		return
	}

	body, err := effectiveMavenModel(ctx, ops, stderr, in)

	var names []string
	if err == nil {
		names, err = domainpublish.MavenReactorCoordinates(body)
	}

	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Warning: the Maven packages are published but not linked to the repository: %v\n", err)

		return
	}

	linkPackages(ctx, forge, stderr, "maven", true, names)
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
