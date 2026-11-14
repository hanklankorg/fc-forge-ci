// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// IndexBOMInventory is one platform reference of an index BOM: the child image
// by digest, and the file name and SHA-256 of its inventory.
type IndexBOMInventory struct {
	Platform string
	ImageRef string
	Name     string
	SHA256   string
}

type indexBOMDocument struct {
	Format   string `json:"bomFormat"`
	Metadata struct {
		Component indexBOMComponent `json:"component"`
	} `json:"metadata"`
	Components []indexBOMComponent `json:"components"`
}

// ReadIndexBOMInventories validates a producer-pinned index BOM's references
// against unchanged local inventories. read is given the file name the index
// BOM names; nothing is fetched.
func ReadIndexBOMInventories(body []byte, indexRef string, read func(string) ([]byte, error)) error {
	var doc indexBOMDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}

	repository, _, _ := strings.Cut(indexRef, "@")
	if !container.ValidDigestPinnedRef(indexRef) || doc.Format != cycloneDXFormat || doc.Metadata.Component.Name != repository || doc.Metadata.Component.Ref != indexRef || len(doc.Components) == 0 {
		return fmt.Errorf("index SBOM does not identify its ledger image and inventories: %w", errs.ErrValidation)
	}

	for _, component := range doc.Components {
		inventory, err := indexBOMInventory(component)
		if err != nil {
			return err
		}

		body, err := read(inventory.Name)
		if err != nil {
			return err
		}
		// The same producer checksum and CycloneDX admission used in composition.
		if err := validateIndexBOMPart(repository, IndexBOMPart{ImageRef: inventory.ImageRef, Name: inventory.Name, SHA256: inventory.SHA256, Document: body}); err != nil {
			return err
		}
	}

	return nil
}

// IndexBOMInventories returns the platform references of an index BOM and the
// digest-pinned index it describes. The bool is false for a CycloneDX
// document that is no index BOM, such as the inventory of one image: none of
// its components carries a BOM reference. A document with one such reference
// must be a complete index BOM.
func IndexBOMInventories(body []byte) (string, []IndexBOMInventory, bool, error) {
	var doc indexBOMDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", nil, false, fmt.Errorf("image SBOM must be a JSON document: %w: %w", err, errs.ErrMalformedInput)
	}

	if !slices.ContainsFunc(doc.Components, func(component indexBOMComponent) bool {
		return slices.ContainsFunc(component.References, func(reference indexBOMReference) bool { return reference.Type == bomReferenceType })
	}) {
		return "", nil, false, nil
	}

	indexRef := doc.Metadata.Component.Ref

	repository, _, _ := strings.Cut(indexRef, "@")
	if !container.ValidDigestPinnedRef(indexRef) || doc.Format != cycloneDXFormat || doc.Metadata.Component.Name != repository {
		return "", nil, false, fmt.Errorf("index SBOM does not identify its image: %w", errs.ErrValidation)
	}

	inventories := make([]IndexBOMInventory, 0, len(doc.Components))

	for _, component := range doc.Components {
		inventory, err := indexBOMInventory(component)
		if err != nil {
			return "", nil, false, err
		}

		if !container.ValidDigestPinnedRef(inventory.ImageRef) || !strings.HasPrefix(inventory.ImageRef, repository+"@") || !container.ValidSHA256Hex(inventory.SHA256) {
			return "", nil, false, fmt.Errorf("index SBOM platform is outside the index repository or not pinned: %w", errs.ErrValidation)
		}

		inventories = append(inventories, inventory)
	}

	return indexRef, inventories, true, nil
}

// indexBOMInventory reads the one checksum-bound BOM reference of a platform.
func indexBOMInventory(component indexBOMComponent) (IndexBOMInventory, error) {
	if len(component.References) != 1 || component.References[0].Type != bomReferenceType || len(component.References[0].Hashes) != 1 || component.References[0].Hashes[0].Algorithm != "SHA-256" {
		return IndexBOMInventory{}, fmt.Errorf("index SBOM requires one checksum-bound BOM reference per platform: %w", errs.ErrValidation)
	}

	reference := component.References[0]
	if err := ValidateBOMFileName(reference.URL); err != nil {
		return IndexBOMInventory{}, err
	}

	inventory := IndexBOMInventory{ImageRef: component.Name, Name: reference.URL, SHA256: reference.Hashes[0].Content}

	for _, property := range component.Properties {
		if property.Name == "oci:platform" {
			inventory.Platform = property.Value
		}
	}

	return inventory, nil
}
