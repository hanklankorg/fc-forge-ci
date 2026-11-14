// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// PlatformDigest is one immutable child of an expected OCI image index.
type PlatformDigest struct {
	Platform string `json:"platform"`
	Digest   string `json:"digest"`
}

type checkedIndexDescriptor struct {
	Digest   string `json:"digest"`
	Platform struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	} `json:"platform"`
}

func (child checkedIndexDescriptor) matches(want PlatformDigest) bool {
	platform, _ := ParsePlatform(want.Platform) // validated before matching

	return child.Digest == want.Digest && child.Platform.OS == platform.OS &&
		child.Platform.Architecture == platform.Arch &&
		(platform.Variant == "" || child.Platform.Variant == platform.Variant)
}

// ValidateImageIndex requires exactly the expected platform/digest pairs. A
// single-image manifest is accepted only when its own bytes match the sole
// expected child. Callers must inspect that child's actual config platform.
func ValidateImageIndex(raw []byte, expected []PlatformDigest) error {
	if err := validateExpectedPlatforms(expected); err != nil {
		return err
	}

	var document struct {
		SchemaVersion int                      `json:"schemaVersion"`
		MediaType     types.MediaType          `json:"mediaType"`
		Manifests     []checkedIndexDescriptor `json:"manifests"`
	}

	if err := json.Unmarshal(raw, &document); err != nil || document.SchemaVersion != 2 {
		return fmt.Errorf("image manifest must be a schema-version 2 JSON object: %w", errs.ErrMalformedInput)
	}

	switch document.MediaType {
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		return validateSingleImage(raw, document.Manifests, expected)
	case types.OCIImageIndex, types.DockerManifestList:
		return matchIndexChildren(document.Manifests, expected)
	default:
		return fmt.Errorf("unsupported image index media type: %w", errs.ErrMalformedInput)
	}
}

func validateSingleImage(raw []byte, children []checkedIndexDescriptor, expected []PlatformDigest) error {
	if len(children) != 0 || len(expected) != 1 || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != expected[0].Digest {
		return fmt.Errorf("single image does not match the expected child digest: %w", errs.ErrValidation)
	}

	return nil
}

func matchIndexChildren(children []checkedIndexDescriptor, expected []PlatformDigest) error {
	if len(children) != len(expected) {
		return fmt.Errorf("image index must contain exactly %d children: %w", len(expected), errs.ErrValidation)
	}

	seen := make(map[string]bool, len(expected))

	for _, child := range children {
		matched := false

		for _, want := range expected {
			if child.matches(want) {
				if seen[want.Platform] {
					return fmt.Errorf("duplicate index platform %s: %w", want.Platform, errs.ErrValidation)
				}

				seen[want.Platform], matched = true, true

				break
			}
		}

		if !matched {
			return fmt.Errorf("index contains an unexpected platform or child digest: %w", errs.ErrValidation)
		}
	}

	return nil
}

func validateExpectedPlatforms(expected []PlatformDigest) error {
	if len(expected) == 0 {
		return fmt.Errorf("expected image platforms are empty: %w", errs.ErrUsage)
	}

	platforms, digests := map[string]bool{}, map[string]bool{}

	for _, child := range expected {
		if _, err := ParsePlatform(child.Platform); err != nil || strings.ContainsAny(child.Platform, " \t\r\n") || strings.HasSuffix(child.Platform, "/") || !ValidDigest(child.Digest) {
			return fmt.Errorf("invalid expected platform or digest: %w", errs.ErrUsage)
		}

		if platforms[child.Platform] || digests[child.Digest] {
			return fmt.Errorf("expected platforms and digests must be distinct: %w", errs.ErrUsage)
		}

		platforms[child.Platform], digests[child.Digest] = true, true
	}

	return nil
}
