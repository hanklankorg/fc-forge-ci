// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

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
