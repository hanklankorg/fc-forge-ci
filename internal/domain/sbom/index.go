// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const cycloneDXFormat = "CycloneDX"

// bomReferenceType is the CycloneDX external reference type of a BOM.
const bomReferenceType = "bom"

// IndexBOMPart binds an unchanged native CycloneDX document to its platform
// and immutable image. Name is the document's file name in the release's SBOM
// bundle: the index BOM names no location, so the reference holds wherever the
// bundle or the image is read from. SHA256 is producer-owned.
type IndexBOMPart struct {
	Platform string
	ImageRef string
	Name     string
	SHA256   string
	Document []byte
}

type indexBOMComponent struct {
	Type       string              `json:"type"`
	Name       string              `json:"name"`
	Ref        string              `json:"bom-ref"`
	Properties []indexBOMProperty  `json:"properties,omitempty"`
	References []indexBOMReference `json:"externalReferences,omitempty"`
}

type indexBOMProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type indexBOMReference struct {
	Type   string         `json:"type"`
	URL    string         `json:"url"`
	Hashes []indexBOMHash `json:"hashes"`
}

type indexBOMHash struct {
	Algorithm string `json:"alg"`
	Content   string `json:"content"`
}

// ComposeIndexBOM produces a deterministic CycloneDX 1.6 inventory of the index
// children. Each child is named by its digest and carries one checksum-bound
// BOM reference: the inventory's file name in the release's SBOM bundle and its
// SHA-256. It does not flatten or rewrite native component/dependency
// identities or authenticate the release. The caller writes the referenced
// documents into the SBOM bundle beside the index BOM and attests each on its
// child digest.
func ComposeIndexBOM(indexRef string, indexManifest []byte, parts []IndexBOMPart) ([]byte, error) {
	if !container.ValidDigestPinnedRef(indexRef) {
		return nil, fmt.Errorf("index SBOM requires a tag-free digest reference: %w", errs.ErrUsage)
	}

	repository, _, _ := strings.Cut(indexRef, "@")
	if indexRef != fmt.Sprintf("%s@sha256:%x", repository, sha256.Sum256(indexManifest)) {
		return nil, fmt.Errorf("index bytes do not match the supplied reference: %w", errs.ErrValidation)
	}

	expected, err := validateIndexBOMParts(repository, parts)
	if err != nil {
		return nil, err
	}

	if err = container.ValidateImageIndex(indexManifest, expected); err != nil {
		return nil, err
	}

	ordered := slices.Clone(parts)
	slices.SortFunc(ordered, func(a, b IndexBOMPart) int { return strings.Compare(a.Platform, b.Platform) })

	components := make([]indexBOMComponent, 0, len(parts))
	for _, part := range ordered {
		components = append(components, indexBOMComponent{
			Type: "container", Name: part.ImageRef, Ref: part.ImageRef + "#" + part.Platform,
			Properties: []indexBOMProperty{{Name: "oci:platform", Value: part.Platform}},
			References: []indexBOMReference{{
				Type: bomReferenceType, URL: part.Name,
				Hashes: []indexBOMHash{{Algorithm: "SHA-256", Content: part.SHA256}},
			}},
		})
	}

	document := map[string]any{
		"bomFormat": cycloneDXFormat, "specVersion": "1.6", "version": 1,
		"metadata":   map[string]any{"component": indexBOMComponent{Type: "container", Name: repository, Ref: indexRef}},
		"components": components,
	}

	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode index SBOM: %w", err)
	}

	return append(body, '\n'), nil
}

func validateIndexBOMParts(repository string, parts []IndexBOMPart) ([]container.PlatformDigest, error) {
	expected := make([]container.PlatformDigest, 0, len(parts))
	names := make(map[string]string, len(parts))

	for _, part := range parts {
		if err := validateIndexBOMPart(repository, part); err != nil {
			return nil, err
		}

		if previous, exists := names[part.Name]; exists && previous != part.SHA256 {
			return nil, fmt.Errorf("one SBOM file name cannot identify different document bytes: %w", errs.ErrValidation)
		}

		names[part.Name] = part.SHA256
		expected = append(expected, container.PlatformDigest{Platform: part.Platform, Digest: strings.TrimPrefix(part.ImageRef, repository+"@")})
	}

	return expected, nil
}

func validateIndexBOMPart(repository string, part IndexBOMPart) error {
	if !container.ValidDigestPinnedRef(part.ImageRef) || !strings.HasPrefix(part.ImageRef, repository+"@") {
		return fmt.Errorf("native SBOM image is outside the index repository or not digest-pinned: %w", errs.ErrValidation)
	}

	if !container.ValidSHA256Hex(part.SHA256) || fmt.Sprintf("%x", sha256.Sum256(part.Document)) != part.SHA256 {
		return fmt.Errorf("native SBOM does not match its producer checksum: %w", errs.ErrValidation)
	}

	var header struct {
		Format string `json:"bomFormat"`
		Spec   string `json:"specVersion"`
	}

	if err := json.Unmarshal(part.Document, &header); err != nil || header.Format != cycloneDXFormat || header.Spec == "" {
		return fmt.Errorf("native SBOM must be a CycloneDX JSON document: %w", errs.ErrMalformedInput)
	}

	return ValidateBOMFileName(part.Name)
}

// ValidateBOMFileName admits the name an inventory has in the SBOM bundle: one
// plain file name, so the reference cannot name a location, a path or a host.
func ValidateBOMFileName(name string) error {
	if name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "/\\:?#%@ \t\r\n") {
		return fmt.Errorf("native SBOM reference must be a plain file name in the SBOM bundle: %w", errs.ErrUsage)
	}

	return nil
}
