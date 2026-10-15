// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah

import (
	"context"
	"io"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

// BuildSignerImage builds one architecture of a two-phase multi-arch image into
// local buildah storage under the supplied local tag.
func (a *Adapter) BuildSignerImage(ctx context.Context, req container.SignerImageBuildToolRequest, out io.Writer) error {
	args := append(a.global("bud", "--authfile", req.AuthFile), chrootIsolation()...)
	args = append(args,
		"--pull=missing",
		"--layers",
		"--platform", req.Platform,
		"--label", "org.opencontainers.image.source="+req.SourceURL,
		"--label", "org.opencontainers.image.revision="+req.Revision,
	)

	if req.Title != "" {
		args = append(args, "--label", "org.opencontainers.image.title="+req.Title)
	}

	args = append(args,
		"--tag", req.LocalImage,
		"--file", req.Containerfile,
		req.Context,
	)

	return a.run(ctx, out, args...)
}
