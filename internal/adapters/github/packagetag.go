// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// GHCRHost is GitHub's container registry, which deletes nothing through the
// registry API; its versions are removed through the Packages API.
const GHCRHost = "ghcr.io"

// PackageTagDeleter deletes a GHCR tag through the Packages API, satisfying
// provider.TagDeleter for an external registry on ghcr.io.
//
// GHCR cannot remove one tag of a version: deleting the version deletes every
// tag it holds. A staging or candidate tag usually shares its manifest with
// the promoted tag, so the version is deleted only when the tag is the only one
// it holds. Otherwise, and when the token lacks delete:packages, the tag stays
// and the error wraps errs.ErrUnsupported, which cleanup reports and goes past:
// the release is complete and only a tag remains.
type PackageTagDeleter struct {
	Packages *Packages
}

// DeleteTag removes the version that holds tag ref, when it holds nothing else.
func (d PackageTagDeleter) DeleteTag(ctx context.Context, ref string) error {
	parsed, err := container.ParseTaggedRef(ref)
	if err != nil {
		return err
	}

	if parsed.Host != GHCRHost {
		return fmt.Errorf("%s is not on %s; the GitHub Packages API manages only GHCR: %w", ref, GHCRHost, errs.ErrUsage)
	}

	owner, name, _ := strings.Cut(parsed.Path, "/")

	versions, err := d.Packages.PackageVersions(ctx, owner, name)
	if errors.Is(err, errs.ErrMissingInput) {
		// No package means no tag: the state cleanup wants.
		return nil
	}

	if err != nil {
		return keepTag(ref, err)
	}

	for _, version := range versions {
		if !slices.Contains(version.Tags, parsed.Tag) {
			continue
		}

		if others := slices.DeleteFunc(slices.Clone(version.Tags), func(tag string) bool { return tag == parsed.Tag }); len(others) != 0 {
			return fmt.Errorf("%s: its manifest %s also holds %s, and GHCR deletes a version with all its tags, so it stays: %w",
				ref, version.Digest, strings.Join(others, ", "), errs.ErrUnsupported)
		}

		if err := d.Packages.DeletePackageVersion(ctx, owner, name, version.ID); err != nil {
			if errors.Is(err, errs.ErrMissingInput) {
				return nil
			}

			return keepTag(ref, err)
		}

		return nil
	}

	return nil
}

// keepTag turns a refusal the release cannot act on into a kept tag: a token
// without read:packages or delete:packages leaves the tag for the operator.
func keepTag(ref string, err error) error {
	if errors.Is(err, errs.ErrPermissionDenied) {
		return fmt.Errorf("%s stays: the registry token cannot delete GHCR package versions (it needs delete:packages): %w: %w",
			ref, err, errs.ErrUnsupported)
	}

	return err
}
