// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package runtimetags_test

import (
	"github.com/diggsweden/forge-ci/internal/runtimetags"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRuntimeTokenBoundary_DoesNotRewritePartialPins(t *testing.T) {
	t.Parallel()

	for _, ref := range []string{"forge-ci-runtime-base:v1.2.3+build", "forge-ci-runtime-base:v1.2.3-rc.1", "not-forge-ci-runtime-base:v1.2.3", "forge-ci-runtimeother:v1.2.3", "forge-ci-runtime-base:v1.2.3@sha256:abc"} {
		got, count := runtimetags.Rewrite("image: \""+ref+"\"", "v4.0.0")
		require.Equal(t, "image: \""+ref+"\"", got)
		require.Zero(t, count)
	}

	require.Equal(t, []string{"v1.2.3+build"}, runtimetags.Tags("ghcr.io/org/forge-ci-runtime-base:v1.2.3+build"))
	require.Empty(t, runtimetags.Tags("not-forge-ci-runtime-base:v1.2.3"))

	got, count := runtimetags.Rewrite("(forge-ci-runtime-base:v1.2.3), forge-ci-runtime:v2.0.0", "v4.0.0")
	require.Equal(t, "(forge-ci-runtime-base:v4.0.0), forge-ci-runtime:v4.0.0", got)
	require.Equal(t, 2, count)
}
