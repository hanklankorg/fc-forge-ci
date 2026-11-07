// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ReadIndexBOMInventories validates a producer-pinned index BOM's references
// against unchanged local inventories. read never needs to fetch a URL.
func ReadIndexBOMInventories(body []byte, indexRef string, read func(string) ([]byte, error)) error {
	var doc struct {
		Format   string `json:"bomFormat"`
		Metadata struct {
			Component indexBOMComponent `json:"component"`
		} `json:"metadata"`
		Components []indexBOMComponent `json:"components"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}
	repository, _, _ := strings.Cut(indexRef, "@")
	if !container.ValidDigestPinnedRef(indexRef) || doc.Format != "CycloneDX" || doc.Metadata.Component.Name != repository || doc.Metadata.Component.Ref != indexRef || len(doc.Components) == 0 {
		return fmt.Errorf("index SBOM does not identify its ledger image and inventories: %w", errs.ErrValidation)
	}
	for _, component := range doc.Components {
		if len(component.References) != 1 || component.References[0].Type != "bom" || len(component.References[0].Hashes) != 1 || component.References[0].Hashes[0].Algorithm != "SHA-256" {
			return fmt.Errorf("index SBOM requires one checksum-bound BOM reference per platform: %w", errs.ErrValidation)
		}
		reference := component.References[0]
		if err := ValidatePublicBOMURL(reference.URL); err != nil {
			return err
		}
		body, err := read(reference.URL)
		if err != nil {
			return err
		}
		// The same producer checksum and CycloneDX admission used in composition.
		if err := validateIndexBOMPart(repository, IndexBOMPart{ImageRef: component.Name, URL: reference.URL, SHA256: reference.Hashes[0].Content, Document: body}); err != nil {
			return err
		}
	}
	return nil
}
