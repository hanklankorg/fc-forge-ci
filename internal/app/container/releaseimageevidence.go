// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"cmp"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// releaseImageEvidenceMapping names the publisher-relative SBOM and its literal
// signer destination, together with the separate ledger destination. It does
// not admit artifacts or enable a native base handoff.
type releaseImageEvidenceMapping struct {
	outputSBOM string
	signerSBOM string
	ledger     string
}

func mapReleaseImageEvidence(image pipeline.ReleaseImage) (releaseImageEvidenceMapping, error) {
	kind := imageledger.ImageKind(cmp.Or(image.ImageKind, string(imageledger.ImageKindRelease)))
	var root, ledger string
	switch kind {
	case imageledger.ImageKindRelease:
		root, ledger = "dist/", "dist/release-images.json"
	case imageledger.ImageKindBase:
		root, ledger = "premade-sboms/", "dist/base-images.json"
	default:
		return releaseImageEvidenceMapping{}, fmt.Errorf("unsupported image evidence kind %q: %w", kind, errs.ErrInvalidConfig)
	}
	canonical := imageledger.DefaultSBOMPath(kind, image.Flavor, cmp.Or(image.Role, image.Name))
	name, ok := strings.CutPrefix(canonical, root)
	if !ok || name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "\\\r\n\x00") {
		return releaseImageEvidenceMapping{}, fmt.Errorf("image SBOM has no canonical evidence mapping: %w", errs.ErrInvalidConfig)
	}
	output := name
	if kind == imageledger.ImageKindBase {
		output = canonical
	}
	return releaseImageEvidenceMapping{outputSBOM: output, signerSBOM: canonical, ledger: ledger}, nil
}
