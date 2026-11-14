// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"errors"
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

var (
	errDestinationsOverlap = errors.New("snapshot artifact destinations overlap")
	errUploadsConflict     = errors.New("snapshot artifact uploads conflict at destination")
)

// ArtifactTransfers projects exact required uploads into the existing Go
// downloader contract. Repeated architectures/images share identical transfers;
// one upload may be required at different destinations (one item per pair).
func (p Plan) ArtifactTransfers() (pipeline.ArtifactTransferPlan, error) {
	if err := p.Validate(); err != nil {
		return pipeline.ArtifactTransferPlan{}, err
	}

	return p.artifactTransfers()
}

func (p Plan) artifactTransfers() (pipeline.ArtifactTransferPlan, error) {
	plan := pipeline.ArtifactTransferPlan{Version: pipeline.ArtifactTransferPlanVersion, Items: []pipeline.ArtifactTransfer{}}
	seen := map[string]bool{}
	destinations := map[string]string{}

	for _, build := range p.Builds {
		for _, dep := range build.RequiredArtifacts {
			for destination := range destinations {
				if destination != dep.Destination && (pathOverlaps(destination, dep.Destination)) {
					return plan, fmt.Errorf("%w: %s and %s", errDestinationsOverlap, destination, dep.Destination)
				}
			}

			if name, ok := destinations[dep.Destination]; ok && name != dep.ArtifactName {
				return plan, fmt.Errorf("%w %s", errUploadsConflict, dep.Destination)
			}

			destinations[dep.Destination] = dep.ArtifactName

			key := dep.Name
			if seen[key] {
				continue
			}

			seen[key] = true

			item := pipeline.ArtifactTransfer{Kind: pipeline.ArtifactTransferBuildArtifact, Name: dep.ArtifactName, Path: DefaultDownloadDir + "/" + dep.Name, Required: true}
			if err := pipeline.ValidateArtifactTransferItem(item); err != nil {
				return plan, err
			}

			plan.Items = append(plan.Items, item)
		}
	}

	return plan, nil
}

func pathOverlaps(a, b string) bool {
	return len(a) > len(b) && a[:len(b)] == b && a[len(b)] == '/' || len(b) > len(a) && b[:len(a)] == a && b[len(a)] == '/'
}
