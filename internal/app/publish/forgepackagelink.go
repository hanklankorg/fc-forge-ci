// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish

import (
	"context"
	"fmt"
	"io"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// linkPackages links the packages a run has published to the repository it
// ran in, on a forge that keeps packages under their owner. Any other forge
// does not implement the role and nothing happens.
//
// The packages are published by the time this runs, and a publication is not
// taken back. A link the forge refuses is therefore a warning that names the
// package and the reason, and the publication stays a success: the package
// is complete and installable without the link, and the next publication of
// the package links it.
func linkPackages(ctx context.Context, forge any, stderr io.Writer, packageType string, names ...string) {
	linker, ok := forge.(provider.ForgePackageLinker)
	if !ok {
		return
	}

	for _, name := range names {
		if err := linker.LinkForgePackage(ctx, packageType, name); err != nil {
			_, _ = fmt.Fprintf(stderr, "Warning: %s package %s is published but not linked to the repository: %v\n", packageType, name, err)
		}
	}
}

// LinkMavenPublications links the Maven packages of an uploaded publication
// directory to the run's repository; see linkPackages for a refused link.
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

	linkPackages(ctx, forge, stderr, "maven", names...)
}
