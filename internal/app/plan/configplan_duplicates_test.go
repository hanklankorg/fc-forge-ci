// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	"strings"
	"testing"

	appplan "github.com/diggsweden/forge-ci/internal/app/plan"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/stretchr/testify/require"
)

func TestConfigPlanBoundary_DuplicateConsumedMembers(t *testing.T) {
	t.Parallel()

	// pipeline.DecodeConfigPlan owns which members and spellings count as
	// duplicates; these consumers show only that a refused plan has no effect.
	const (
		member    = `"settings_path":"settings.xml"`
		duplicate = `"settings_path":"other.xml"`
	)

	raw := producedBoundaryConfigPlan(t)
	require.Contains(t, raw, member)

	t.Run("duplicate", func(t *testing.T) {
		assertDuplicatePlanRefusal(t, strings.Replace(raw, member, member+","+duplicate, 1), "config-plan.artifacts.all[1].maven.settings_path has duplicate consumed member")
	})
	// Duplicate diagnostics must precede semantic version/projection errors.
	assertDuplicatePlanRefusal(t, `{"version":999,"artifacts":{"maven":[],"MAVEN":[{}]}}`, "config-plan.artifacts.maven has duplicate consumed member")
}

func assertDuplicatePlanRefusal(t *testing.T, raw, reason string) {
	t.Helper()

	for _, seed := range []string{"", "existing"} {
		var events []string

		sink := &patternSink{Sink: fakeoutputsink.New(t), events: &events}
		writer := &patternWriter{events: &events}
		summary := &executionSummary{writer: writer, events: &events}

		if seed != "" {
			require.NoError(t, sink.Sink.Set(t.Context(), "canary", seed))
			_, err := writer.WriteString(seed)
			require.NoError(t, err)
		}

		before := sink.AllScalar()

		t.Run("release/"+seed, func(t *testing.T) {
			plan, err := appplan.Release(t.Context(), sink, summary, appplan.ReleaseInput{ConfigPlanJSON: raw, ReleaseSBOMs: "all", ReleaseSignArtifacts: true, RefName: "v1.2.3"})
			require.Empty(t, events)
			require.Equal(t, before, sink.AllScalar())
			require.Equal(t, seed, writer.String())
			require.Nil(t, plan)
			require.ErrorIs(t, err, errs.ErrInvalidConfig)
			require.ErrorContains(t, err, reason)
		})
		t.Run("snapshot/"+seed, func(t *testing.T) {
			plan, err := appplan.SnapshotRelease(t.Context(), sink, appplan.SnapshotReleaseInput{ConfigPlanJSON: raw, ProjectType: "maven", SBOMs: "none", PublishNPM: false})
			require.Empty(t, events)
			require.Equal(t, before, sink.AllScalar())
			require.Equal(t, seed, writer.String())
			require.Nil(t, plan)
			require.ErrorIs(t, err, errs.ErrInvalidConfig)
			require.ErrorContains(t, err, reason)
		})
	}
}
