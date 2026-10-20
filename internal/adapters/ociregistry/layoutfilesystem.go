// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"context"
	"io"

	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

// ExportLayoutFilesystem reads the effective filesystem of the retained image
// after the existing offline blob verification. Layer ordering and whiteouts
// are handled by go-containerregistry, with no rebuild, pull or container engine.
func (a *Adapter) ExportLayoutFilesystem(ctx context.Context, directory string) (io.ReadCloser, error) {
	if _, err := a.InspectLayout(ctx, directory); err != nil {
		return nil, err
	}
	image, err := layoutImage(directory)
	if err != nil {
		return nil, err
	}
	return mutate.Extract(image), nil
}
