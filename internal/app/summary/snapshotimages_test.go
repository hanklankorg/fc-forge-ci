// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package summary_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	appsummary "github.com/diggsweden/forge-ci/internal/app/summary"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/stretchr/testify/require"
)

func TestSnapshotImageSummaryUsesIndependentJobResult(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	tag, err := version.ComposeSnapshotContainerTag("1.2.3", "feature/manual", sha[:7])
	require.NoError(t, err)
	r := snapshotimage.Receipt{Version: 1, SourceSHA: sha, SourceBranch: "feature/manual", Name: "server", ImageRepository: "registry.example/team/server", Tag: tag, Digest: "sha256:" + strings.Repeat("b", 64), Platforms: []string{"linux/amd64", "linux/arm64"}}
	r.Ref = r.ImageRepository + "@" + r.Digest
	body, err := json.Marshal([]snapshotimage.Receipt{r})
	require.NoError(t, err)
	for _, tc := range []struct {
		result, inventory, icon string
		published               bool
	}{
		{"success", string(body), "✓", true},
		{"failure", string(body), "✗", false},
		{"cancelled", "", "✗", false},
		{"skipped", "", "−", false},
		{"success", "[]", "✓", false},
	} {
		t.Run(tc.result+tc.inventory[:min(2, len(tc.inventory))], func(t *testing.T) {
			sink := &fakeSummarySink{}
			err := appsummary.SnapshotReleaseSummary(t.Context(), sink, &bytes.Buffer{}, appsummary.SnapshotReleaseSummaryInput{ReleaseSHA: sha, ReleaseRef: r.SourceBranch, SnapshotImageResult: tc.result, ContainerImagesJSON: tc.inventory})
			require.NoError(t, err)
			require.Contains(t, sink.buf.String(), "| Snapshot Images | "+tc.icon+" |")
			require.Equal(t, tc.published, strings.Contains(sink.buf.String(), r.Ref))
			if tc.published {
				require.Contains(t, sink.buf.String(), "linux/amd64, linux/arm64")
			}
		})
	}
	for _, mutate := range []func(*snapshotimage.Receipt){
		func(r *snapshotimage.Receipt) { r.SourceSHA = strings.Repeat("c", 40) },
		func(r *snapshotimage.Receipt) { r.SourceBranch = "main" },
		func(r *snapshotimage.Receipt) { r.Ref = r.ImageRepository + ":latest" },
		func(r *snapshotimage.Receipt) { r.Digest = "sha256:bad" },
		func(r *snapshotimage.Receipt) { r.Tag = "latest" },
		func(r *snapshotimage.Receipt) { r.Platforms = []string{"linux/amd64", "linux/amd64"} },
		func(r *snapshotimage.Receipt) { r.Name = "bad\n| injected | ✓ |" },
	} {
		bad := r
		mutate(&bad)
		raw, err := json.Marshal([]snapshotimage.Receipt{bad})
		require.NoError(t, err)
		sink := &fakeSummarySink{}
		err = appsummary.SnapshotReleaseSummary(t.Context(), sink, &bytes.Buffer{}, appsummary.SnapshotReleaseSummaryInput{ReleaseSHA: sha, ReleaseRef: r.SourceBranch, SnapshotImageResult: "success", ContainerImagesJSON: string(raw)})
		require.ErrorIs(t, err, errs.ErrMalformedInput)
		require.Empty(t, sink.buf.String())
	}
}

func TestSnapshotImageSummaryRejectsMalformedOrMissingInventory(t *testing.T) {
	t.Parallel()
	for _, inventory := range []string{"", "null", "{}", "[] []", `[{"unexpected":true}]`, "[null]"} {
		sink := &fakeSummarySink{}
		err := appsummary.SnapshotReleaseSummary(t.Context(), sink, &bytes.Buffer{}, appsummary.SnapshotReleaseSummaryInput{SnapshotImageResult: "success", ContainerImagesJSON: inventory})
		require.ErrorIs(t, err, errs.ErrMalformedInput, inventory)
		require.Empty(t, sink.buf.String())
	}
}
