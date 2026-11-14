// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package runner implements `forge-ci platform request-runner`: the first job
// of a workflow whose later job builds container images asks a lender for an
// ephemeral runner, then that job waits on the lent runner's labels.
package runner

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/lender"
)

// ttlBounds keep a forgotten runner from idling for hours and a request from
// giving up before a VM has booted.
const (
	minTTLMinutes = 2
	maxTTLMinutes = 180
)

// RequestInput drives Request.
type RequestInput struct {
	// Arch is the architecture of the job the runner is for.
	Arch string
	// TTLMinutes is how long the lent runner waits for that job.
	TTLMinutes int
	// LenderName names the lender in the summary, e.g. "github:example-org/runner-lender".
	// Empty means no lender is configured and nothing is requested.
	LenderName string
}

// Request asks lend for one ephemeral runner. A nil lend is the installation
// that has its own container-capable runners: the job runs, says so, and the
// waiting job finds those runners by the same labels. Either way the summary
// names what the next job waits for.
func Request(ctx context.Context, lend lender.Lender, out io.Writer, in RequestInput) error {
	arch, err := lender.ParseArch(in.Arch)
	if err != nil {
		return err
	}

	if in.TTLMinutes < minTTLMinutes || in.TTLMinutes > maxTTLMinutes {
		return fmt.Errorf("runner ttl must be %d to %d minutes, got %d: %w", minTTLMinutes, maxTTLMinutes, in.TTLMinutes, errs.ErrUsage)
	}

	if lend == nil {
		_, _ = fmt.Fprintf(out, "No runner lender is configured: the linux/%s container job runs on the pool, or on the runners CI_RUNNER_LABEL_CONTAINER names when that is set.\n", arch)

		return nil
	}

	if in.LenderName == "" {
		return fmt.Errorf("a lender needs a name for the summary: %w", errs.ErrUsage)
	}

	req := lender.Request{Arch: arch, TTL: time.Duration(in.TTLMinutes) * time.Minute}
	if err := lend.Request(ctx, req); err != nil {
		return fmt.Errorf("request a linux/%s runner from %s: %w", arch, in.LenderName, err)
	}

	_, _ = fmt.Fprintf(out, "Requested one ephemeral linux/%s runner from %s; it waits up to %d minutes for the container job that needs it.\n", arch, in.LenderName, in.TTLMinutes)

	return nil
}
