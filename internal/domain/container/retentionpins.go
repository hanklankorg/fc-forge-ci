// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ProtectedPackage groups the union of consumer pins for one GHCR package.
type ProtectedPackage struct {
	Repository string
	Digests    []string
}

// ParsePackageRetentionPins admits the complete caller inventory before effects.
// Repeated repositories are grouped; repeated pins are idempotent. Input order
// is retained so previews remain deterministic without weakening protection.
func ParsePackageRetentionPins(raw string) ([]ProtectedPackage, error) {
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("protected package inventory exceeds 1 MiB: %w", errs.ErrUsage)
	}

	var input struct {
		Include []struct {
			Repository string `json:"repository"`
			Digest     string `json:"digest"`
		} `json:"include"`
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("invalid protected package inventory: %w: %w", err, errs.ErrUsage)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || len(input.Include) == 0 {
		return nil, fmt.Errorf("protected package inventory requires one nonempty include array: %w", errs.ErrUsage)
	}

	groups := make([]ProtectedPackage, 0, len(input.Include))
	indices := make(map[string]int)

	for _, pin := range input.Include {
		if !strings.HasPrefix(pin.Repository, "ghcr.io/") || strings.Count(pin.Repository, "/") < 2 || !ValidRepositoryName(pin.Repository) ||
			!strings.HasPrefix(pin.Digest, "sha256:") || !ValidSHA256Hex(strings.TrimPrefix(pin.Digest, "sha256:")) {
			return nil, fmt.Errorf("protected package inventory requires GHCR repositories and sha256 digests: %w", errs.ErrUsage)
		}

		index, found := indices[pin.Repository]
		if !found {
			index = len(groups)
			indices[pin.Repository] = index
			groups = append(groups, ProtectedPackage{Repository: pin.Repository})
		}

		if !slices.Contains(groups[index].Digests, pin.Digest) {
			groups[index].Digests = append(groups[index].Digests, pin.Digest)
		}
	}

	return groups, nil
}
