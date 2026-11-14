// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

type centralProbe struct {
	states         []string
	reads, uploads int
	failFirst      bool
}

func (p *centralProbe) Upload(context.Context, string, bool) (string, error) {
	p.uploads++

	return "deployment-1", nil
}
func (p *centralProbe) State(context.Context, string) (string, error) {
	p.reads++
	if p.failFirst && p.reads == 1 {
		return "", errs.ErrDependencyUnavailable
	}

	state := p.states[0]
	if len(p.states) > 1 {
		p.states = p.states[1:]
	}

	return state, nil
}

func TestCentralObservationNeverClaimsValidationIsPublication(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                        string
		states                      []string
		automatic, transient, fails bool
	}{
		{"published", []string{"VALIDATED", "PUBLISHING", "PUBLISHED"}, true, false, false},
		{"manual", []string{"VALIDATED"}, false, false, false},
		{"retry read", []string{"PUBLISHED"}, true, true, false},
		{"failed", []string{"FAILED"}, true, false, true},
		{"unknown", []string{"UNRECOGNIZED"}, true, false, true},
		{"unconfirmed", []string{"VALIDATED"}, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			probe := &centralProbe{states: append([]string{}, tc.states...), failFirst: tc.transient}

			var out bytes.Buffer

			// Only "unconfirmed" must run out of time; a short budget elsewhere
			// fails a loaded test run before the probe reaches its last state.
			budget := time.Minute
			if tc.name == "unconfirmed" {
				budget = 100 * time.Millisecond
			}

			err := apppublish.WaitCentralDeployment(t.Context(), probe, &out, "deployment-1", tc.automatic, budget, time.Millisecond)
			if tc.fails {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Zero(t, probe.uploads, "status retries must never resubmit")

			if tc.name == "published" {
				require.Equal(t, 3, probe.reads)
			}

			if tc.name == "manual" {
				require.Contains(t, out.String(), "manual release is still required")
			}
		})
	}
}

func TestCentralRejectsUnsignedBundleBeforeUpload(t *testing.T) {
	t.Parallel()

	probe := &centralProbe{}
	err := apppublish.SubmitCentralBundle(t.Context(), probe, io.Discard, apppublish.CentralBundleInput{Directory: publicationFixture(t), Version: "1.2.3", Automatic: true})
	require.Error(t, err)
	require.Zero(t, probe.uploads)
	require.Zero(t, probe.reads)
}

func TestCentralRecoveryPreservesPublicationSuccessCondition(t *testing.T) {
	t.Parallel()

	for name, automatic := range map[string]bool{"automatic": true, "manual": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			probe := &centralProbe{states: []string{"VALIDATING"}}
			err := apppublish.WaitCentralDeployment(ctx, probe, io.Discard, "deployment-1", automatic, time.Second, time.Second)
			require.ErrorIs(t, err, context.Canceled)
			require.Zero(t, probe.uploads)

			command := "forge-ci publish maven-central wait --deployment-id deployment-1"
			if automatic {
				command += " --automatic"
			}

			require.ErrorContains(t, err, "resume with "+command+", do not re-upload")
		})
	}
}
