// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cienv_test

import (
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/stretchr/testify/require"
	"strconv"
	"testing"
	"time"
)

func TestCIFixtureBoundary_IsolatesAllAmbientAliases(t *testing.T) {
	for key, value := range map[string]string{"FORGEJO_ACTIONS": "true", "GITHUB_ACTIONS": "true", "GITLAB_CI": "true", "FORGEJO_WORKFLOW_REF": "ambient/workflow", "CI_JOB_URL": "https://ambient.invalid/job"} {
		t.Setenv(key, value)
	}

	for _, variable := range []runcontext.Var{runcontext.RefName(), runcontext.Ref(), runcontext.Repository(), runcontext.ServerURL(), runcontext.CheckoutRef()} {
		for _, key := range variable.Keys() {
			t.Setenv(key, "ambient-canary")
		}
	}

	for _, test := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{"empty", TestNonEmptyEnvSource_SkipsSetButEmpty}, {"first", TestNonEmptyEnvSource_FirstNonEmptyWins}, {"absent", TestNonEmptyEnvSource_AllUnsetIsAbsent}, {"server", TestServerURL_ForgeFallbacks}, {"checkout", TestCheckoutRef_ExplicitOverrideWinsOverCommit},
		{"builder", TestProvenanceBuilderID_CanonicalWorkflowRef}, {"invocation", TestProvenanceInvocationID_RunURL}, {"job", TestProvenanceBuilderID_FallsBackToJobURL},
	} {
		t.Run(test.name, test.fn)
	}
}

func TestEpochBoundary_OnlyRepresentableRFC3339(t *testing.T) {
	testenv.New(t)

	for _, tc := range []struct {
		seconds int64
		want    string
	}{
		{0, "1970-01-01T00:00:00Z"}, {253402300799, "9999-12-31T23:59:59Z"}, {-1, ""}, {253402300800, ""}, {9223372036854775807, ""}, {-9223372036854775808, ""},
	} {
		t.Setenv("SOURCE_DATE_EPOCH", strconv.FormatInt(tc.seconds, 10))

		got, ok := cienv.SourceDateEpochRFC3339()
		require.Equal(t, tc.want != "", ok)
		require.Equal(t, tc.want, got)

		if ok {
			_, err := time.Parse(time.RFC3339, got)
			require.NoError(t, err)
		}
	}
}

// The build timestamp both provenance verbs resolve: the flag value when it is
// RFC3339, else the epoch variable, else a refusal.
func TestStartedOn_FlagThenEpochThenRefusal(t *testing.T) {
	testenv.New(t)

	for _, tc := range []struct {
		name, flag, epoch, want, message string
	}{
		{name: "flag wins over the epoch", flag: "2026-01-02T03:04:05Z", epoch: "0", want: "2026-01-02T03:04:05Z"},
		{name: "epoch without a flag", epoch: "0", want: "1970-01-01T00:00:00Z"},
		{name: "flag that is not RFC3339", flag: "yesterday", epoch: "0", message: `--started-on "yesterday" is not RFC3339`},
		{name: "neither", message: "no build timestamp"},
		{name: "malformed epoch", epoch: "soon", message: "no build timestamp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SOURCE_DATE_EPOCH", tc.epoch)

			got, err := cienv.StartedOn(tc.flag)
			if tc.message != "" {
				require.ErrorIs(t, err, errs.ErrUsage)
				require.ErrorContains(t, err, tc.message)
				require.Empty(t, got)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
