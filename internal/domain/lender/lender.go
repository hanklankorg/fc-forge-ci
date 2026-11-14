// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package lender is the port through which a job asks for an ephemeral
// runner.
//
// A runner pool that shares its kernel between tenants denies user
// namespaces, and rootless Buildah and Podman cannot run without them. A
// lender supplies a machine that permits them: it starts one runner,
// registered to the orchestrating forge for exactly one job, with the
// labels the waiting job requires. Which machine, and how it is started, is
// the adapter's business; the request names only the architecture and how
// long the runner waits for its job.
package lender

import (
	"context"
	"fmt"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// Arch is a Linux CPU architecture a lent runner is requested for.
type Arch string

// The architectures the layer builds and publishes its binary for.
const (
	ArchAMD64 Arch = "amd64"
	ArchARM64 Arch = "arm64"
)

// ParseArch accepts the two architectures the layer runs on.
func ParseArch(raw string) (Arch, error) {
	switch Arch(raw) {
	case ArchAMD64, ArchARM64:
		return Arch(raw), nil
	default:
		return "", fmt.Errorf("runner architecture must be amd64 or arm64, got %q: %w", raw, errs.ErrUsage)
	}
}

// Request is one ephemeral runner.
type Request struct {
	// Arch is the runner's architecture; the lender picks its machine by it.
	Arch Arch
	// TTL bounds how long the runner waits for a job before it gives up and
	// the machine is released.
	TTL time.Duration
}

// Lender starts one ephemeral runner for the orchestrating forge. It
// returns once the request is accepted; the runner registers on its own and
// the forge hands it the waiting job.
type Lender interface {
	Request(ctx context.Context, req Request) error
}
