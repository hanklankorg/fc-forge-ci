// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/diggsweden/forge-ci/internal/app/baseimages"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

type fakeFreshnessResolver struct {
	digests map[string]string
	err     error
}

func (f *fakeFreshnessResolver) ResolveDigest(_ context.Context, ref string) (string, error) {
	if f.err != nil {
		return "", f.err
	}

	return f.digests[ref], nil
}

var errRegistryBlip error = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}

const freshPinned = "docker.io/library/debian:trixie-slim@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func freshnessInput(enforce bool) baseimages.CheckFreshnessInput {
	return baseimages.CheckFreshnessInput{
		ChecksJSON: `[{"name":"DEBIAN_IMAGE","pinned-ref":"` + freshPinned + `","source-tag":"docker.io/library/debian:trixie-slim"}]`,
		Enforce:    enforce,
	}
}

// TestCheckFreshness_MatchingDigestReportsCurrent: when the source tag still
// resolves to the pinned digest there is nothing to do, and the run says so
// both to the operator and to the output sink the workflow reads.
func TestCheckFreshness_MatchingDigestReportsCurrent(t *testing.T) {
	t.Parallel()

	resolver := &fakeFreshnessResolver{digests: map[string]string{
		"docker.io/library/debian:trixie-slim": "sha256:" + strings.Repeat("a", 64),
	}}
	sink := fakeoutputsink.New(t)

	var out, stderr bytes.Buffer

	err := baseimages.CheckFreshness(context.Background(), resolver, sink, &out, &stderr, freshnessInput(true))
	if err != nil {
		t.Fatalf("CheckFreshness() = %v, want nil", err)
	}

	if got := sink.Single("stale-count"); got != "0" {
		t.Errorf("stale-count = %q, want 0", got)
	}

	if !strings.Contains(out.String(), "DEBIAN_IMAGE digest is current") {
		t.Errorf("out = %q, want current message", out.String())
	}
}

// TestCheckFreshness_StaleDigestBlocksOnlyWhenEnforcing: a drifted digest is
// the same finding in both modes; enforce decides whether it stops the build.
// The old name said only "Stale", which is the input rather than the
// behaviour, and hid that the mode is what this test is actually about.
func TestCheckFreshness_StaleDigestBlocksOnlyWhenEnforcing(t *testing.T) {
	t.Parallel()

	resolver := &fakeFreshnessResolver{digests: map[string]string{
		"docker.io/library/debian:trixie-slim": "sha256:" + strings.Repeat("b", 64),
	}}

	t.Run("enforce fails", func(t *testing.T) {
		t.Parallel()

		var out, stderr bytes.Buffer

		err := baseimages.CheckFreshness(context.Background(), resolver, fakeoutputsink.New(t), &out, &stderr, freshnessInput(true))
		if !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("CheckFreshness() = %v, want ErrValidation", err)
		}

		if !strings.Contains(stderr.String(), "ERROR: DEBIAN_IMAGE digest is stale") {
			t.Errorf("stderr = %q, want stale error", stderr.String())
		}
	})

	t.Run("warn-only passes with stale-count output", func(t *testing.T) {
		t.Parallel()

		sink := fakeoutputsink.New(t)

		var out, stderr bytes.Buffer

		err := baseimages.CheckFreshness(context.Background(), resolver, sink, &out, &stderr, freshnessInput(false))
		if err != nil {
			t.Fatalf("CheckFreshness() = %v, want nil", err)
		}

		if got := sink.Single("stale-count"); got != "1" {
			t.Errorf("stale-count = %q, want 1", got)
		}

		if !strings.Contains(stderr.String(), "not blocking") {
			t.Errorf("stderr = %q, want non-blocking warning", stderr.String())
		}
	})
}

// TestCheckFreshness_UnreachableRegistryFailsClosedWhenEnforcing: a registry
// that cannot be reached is not evidence that the pin is current, so enforcing
// treats it as a dependency failure rather than a pass. Warn-only skips it.
func TestCheckFreshness_UnreachableRegistryFailsClosedWhenEnforcing(t *testing.T) {
	t.Parallel()

	resolver := &fakeFreshnessResolver{err: errRegistryBlip}

	t.Run("enforce fails closed", func(t *testing.T) {
		t.Parallel()

		var out, stderr bytes.Buffer

		err := baseimages.CheckFreshness(context.Background(), resolver, fakeoutputsink.New(t), &out, &stderr, freshnessInput(true))
		if !errors.Is(err, errs.ErrDependencyUnavailable) {
			t.Fatalf("CheckFreshness() = %v, want ErrDependencyUnavailable", err)
		}
	})

	// A registry that answered with a refusal was reached: its class stays.
	t.Run("enforce keeps a refusal's class", func(t *testing.T) {
		t.Parallel()

		refused := &fakeFreshnessResolver{err: fmt.Errorf("401 from the registry: %w", errs.ErrPermissionDenied)}

		var out, stderr bytes.Buffer

		err := baseimages.CheckFreshness(context.Background(), refused, fakeoutputsink.New(t), &out, &stderr, freshnessInput(true))
		if !errors.Is(err, errs.ErrPermissionDenied) || errors.Is(err, errs.ErrDependencyUnavailable) {
			t.Fatalf("CheckFreshness() = %v, want permission denied and not unavailable", err)
		}
	})

	// Warn-only does not block, and does not report the pin as current
	// either: it is counted as unchecked, with the cause, and never printed
	// as "digest is current".
	t.Run("warn-only reports the pin as unchecked, not current", func(t *testing.T) {
		t.Parallel()

		sink := fakeoutputsink.New(t)

		var out, stderr bytes.Buffer

		err := baseimages.CheckFreshness(context.Background(), resolver, sink, &out, &stderr, freshnessInput(false))
		if err != nil {
			t.Fatalf("CheckFreshness() = %v, want nil", err)
		}

		if sink.Single("stale-count") != "0" {
			t.Errorf("stale-count = %q, want 0", sink.Single("stale-count"))
		}

		for _, want := range []string{"the pin was not checked (not blocking): " + errRegistryBlip.Error(), "1 pinned container digest(s) could not be compared with upstream"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr = %q, want %q", stderr.String(), want)
			}
		}

		if strings.Contains(out.String(), "digest is current") {
			t.Errorf("out = %q: an unchecked pin was reported current", out.String())
		}
	})
}

func TestCheckFreshness_ConfigErrorsFailInBothModes(t *testing.T) {
	t.Parallel()

	resolver := &fakeFreshnessResolver{}

	cases := map[string]string{
		"unpinned ref":       `[{"name":"X","pinned-ref":"docker.io/library/debian:trixie-slim","source-tag":"docker.io/library/debian:trixie"}]`,
		"digest source tag":  `[{"name":"X","pinned-ref":"` + freshPinned + `","source-tag":"docker.io/library/debian@sha256:abc"}]`,
		"url source tag":     `[{"name":"X","pinned-ref":"` + freshPinned + `","source-tag":"https://docker.io/library/debian:trixie"}]`,
		"tagless source tag": `[{"name":"X","pinned-ref":"` + freshPinned + `","source-tag":"debian"}]`,
		"empty checks":       `[]`,
	}

	for name, checksJSON := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out, stderr bytes.Buffer

			err := baseimages.CheckFreshness(context.Background(), resolver, fakeoutputsink.New(t), &out, &stderr,
				baseimages.CheckFreshnessInput{ChecksJSON: checksJSON, Enforce: false})
			if err == nil {
				t.Fatalf("CheckFreshness(%s) = nil, want config error even in warn-only mode", name)
			}
		})
	}
}
