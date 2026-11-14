// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// PromotionImagesFromLedger admits the publication ledger before projecting
// candidate/final identities. This is data admission, never a registry mutation.
func PromotionImagesFromLedger(body []byte, repository string) ([]BaseImageMetadata, error) {
	entries, err := AdmitBaseImageLedger(body, repository)
	if err != nil {
		return nil, err
	}

	images := make([]BaseImageMetadata, 0, len(entries))
	for _, entry := range entries {
		images = append(images, BaseImageMetadata{Flavor: entry.Flavor, Tag: entry.FinalTag, Ref: entry.Ref, CandidateTag: entry.CandidateTag, CandidateRef: entry.Ref, BaseInputID: entry.BaseInputID, SBOMSHA256: entry.SBOMSHA256})
	}

	return images, nil
}

// BaseInputsFromLedger projects the complete expected-input map that a
// base-image ledger declares: every flavor's input ID and, for a child, the
// parent_base its build recorded. It is the declared lineage that promotion
// then requires in each image's own verified statement, not authentication
// of the parent: a caller with an authenticated parent passes that instead.
func BaseInputsFromLedger(body []byte, repository string) ([]BaseInput, error) {
	entries, err := AdmitBaseImageLedger(body, repository)
	if err != nil {
		return nil, err
	}

	inputs := make([]BaseInput, 0, len(entries))

	for _, entry := range entries {
		input := BaseInput{Flavor: entry.Flavor, BaseInputID: entry.BaseInputID}

		if raw, ok := entry.Provenance["parent_base"]; ok {
			encoded, err := json.Marshal(raw)
			if err != nil {
				return nil, err
			}

			decoder := json.NewDecoder(bytes.NewReader(encoded))
			decoder.DisallowUnknownFields()

			input.ParentBase = new(BaseImageParent)
			if err := decoder.Decode(input.ParentBase); err != nil {
				return nil, fmt.Errorf("base images: ledger parent_base must be {ref,input_id}: %w", errs.ErrValidation)
			}
		}

		inputs = append(inputs, input)
	}

	if _, err := baseInputByFlavor(inputs); err != nil {
		return nil, err
	}

	return inputs, nil
}
