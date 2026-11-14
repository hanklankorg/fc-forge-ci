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
	args := a.global("bud")
	if req.AuthFile != "" {
		args = append(args, "--authfile", req.AuthFile)
	}

	args = append(args, chrootIsolation()...)
	args = append(args, registryRetry()...)
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

	for _, label := range req.Labels {
		args = append(args, "--label", label)
	}

	for _, buildArg := range req.BuildArgs {
		args = append(args, "--build-arg", buildArg)
	}

	if req.Target != "" {
		args = append(args, "--target", req.Target)
	}

	args = append(args, "--tag", req.LocalImage, "--file", req.Containerfile)
	args = append(args, ignoreFileArgs(req.Containerfile)...)
	args = append(args, req.Context)

	return a.run(ctx, out, args...)
}
