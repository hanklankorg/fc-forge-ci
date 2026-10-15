// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package container holds pure container-image domain logic: name
// resolution, namespace policy, OCI-label assembly, binary-extract suffix.
package container

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/repository"
)

// ResolveImageNameInput is the input for ResolveImageName.
type ResolveImageNameInput struct {
	Registry        string // registry authority, e.g. ghcr.io or localhost:5000
	ImageName       string // explicit tag-free repository override
	Repository      string // source namespace/repository, with original spelling
	RepositoryOwner string // namespace for a bare Docker Hub override
	Name            string // optional sub-name, collapsed when equal after lowercasing
}

// ResolveImageName returns a fully qualified, lowercase OCI repository. A
// source-derived path always uses Registry; dots in source names never select
// a registry. Explicit overrides may carry their own registry authority.
// Names that cannot be represented in OCI require an explicit image override;
// punctuation is never silently removed or replaced (which could collide).
func ResolveImageName(in ResolveImageNameInput) (string, error) {
	image := in.ImageName
	if image == "" {
		derived, err := derivedImagePath(in.Repository, in.Name)
		if err != nil {
			return "", err
		}

		image = derived
	} else {
		authority, _, hasPath := strings.Cut(image, "/")
		if hasPath && (strings.EqualFold(authority, "localhost") || strings.ContainsAny(authority, ".:")) {
			return resolvedImageRepository(image)
		}

		if strings.EqualFold(in.Registry, "docker.io") && !hasPath {
			image = in.RepositoryOwner + "/" + image
		}
	}

	if in.Registry == "" || strings.Contains(in.Registry, "/") {
		return "", fmt.Errorf("image registry must be an authority without a URL scheme or path: %w", errs.ErrUsage)
	}

	return resolvedImageRepository(in.Registry + "/" + image)
}

func derivedImagePath(source, name string) (string, error) {
	path, err := repository.Parse(source)
	if err != nil {
		return "", err
	}

	// Compare the lowercase spelling used in the destination, not Unicode
	// case-fold equivalence, which also equates OCI-invalid characters to ASCII.
	name = strings.ToLower(name)
	if name != "" && name != strings.ToLower(path.Name()) {
		return path.String() + "/" + name, nil
	}

	return path.String(), nil
}

func resolvedImageRepository(image string) (string, error) {
	image = strings.ToLower(image)
	if !ValidRepositoryName(image) {
		return "", fmt.Errorf("image name is not a valid OCI repository; supply an explicit valid image repository and matching publication policy: %w", errs.ErrUsage)
	}

	return image, nil
}
