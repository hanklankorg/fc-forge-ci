// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"fmt"
	"strings"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ManifestTagger is the existing registry digest-to-tag copy capability.
type ManifestTagger interface {
	CopyTag(context.Context, string, string) error
	ResolveDigest(context.Context, string) (string, error)
}

// TagManifest exposes an already published (and, when requested, signed)
// immutable manifest at its public aliases. Admit every destination before the
// first copy, and never resolve a mutable transport tag at this boundary.
func TagManifest(ctx context.Context, reg ManifestTagger, source, tags string) error {
	source = strings.TrimSpace(source)
	image, digest, ok := strings.Cut(source, "@")
	if !ok || !domaincontainer.ValidDigest(digest) {
		return fmt.Errorf("manifest tag requires an immutable digest reference: %w", errs.ErrUsage)
	}

	destinations := nonEmptyLines(tags)
	if len(destinations) == 0 {
		return fmt.Errorf("tags is required: %w", errs.ErrUsage)
	}
	if err := domaincontainer.ValidateManifestTags(image, destinations); err != nil {
		return err
	}

	for _, dest := range destinations {
		if err := reg.CopyTag(ctx, source, dest); err != nil {
			return err
		}
	}
	// Read every alias after all copies, so an overlapping publisher cannot
	// silently replace this run's signed identity in the hand-off.
	for _, dest := range destinations {
		actual, err := reg.ResolveDigest(ctx, dest)
		if err != nil {
			return fmt.Errorf("verify promoted manifest %s: %w", dest, err)
		}
		if actual != digest {
			return fmt.Errorf("promoted manifest %s digest mismatch: got %s, want %s: %w", dest, actual, digest, errs.ErrValidation)
		}
	}
	return nil
}
