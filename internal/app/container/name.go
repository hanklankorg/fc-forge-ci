// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package container holds the use-case orchestration for the
// `forge-ci container ...` subcommands. Imports domain only; a use
// case takes its sinks and tools as port parameters, which the CLI binds.
package container

import (
	"context"

	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/container"
)

// ResolveNameInput drives the `forge-ci container ref resolve` use case.
type ResolveNameInput struct {
	Registry        string
	ImageName       string
	Repository      string
	RepositoryOwner string
	Name            string
}

// ResolveName computes the canonical image reference and writes it to the
// output sink as `name=<value>`. Mirrors the
// container image naming contract.
func ResolveName(ctx context.Context, sink ci.OutputSink, in ResolveNameInput) error {
	resolved, err := container.ResolveImageName(container.ResolveImageNameInput{
		Registry:        in.Registry,
		ImageName:       in.ImageName,
		Repository:      in.Repository,
		RepositoryOwner: in.RepositoryOwner,
		Name:            in.Name,
	})
	if err != nil {
		return err
	}

	return sink.Set(ctx, "name", resolved)
}
