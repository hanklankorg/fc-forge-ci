// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package runtimetags_test

import (
	"github.com/diggsweden/forge-ci/internal/runtimetags"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRuntimeTokenBoundary_FindsOnlyWholeReferences(t *testing.T) {
	t.Parallel()

	for ref, want := range map[string][]string{
		"forge-ci-runtime-base:v1.2.3+build":       {"v1.2.3+build"},
		"forge-ci-runtime-base:v1.2.3-rc.1":        {"v1.2.3-rc.1"},
		"forge-ci-runtime-base:v1.2.3@sha256:abc":  {"v1.2.3@sha256:abc"},
		"not-forge-ci-runtime-base:v1.2.3":         {},
		"forge-ci-runtimeother:v1.2.3":             {},
		"ghcr.io/org/forge-ci-runtime-base:v1.2.3": {"v1.2.3"},
	} {
		require.Equal(t, want, runtimetags.Tags("image: \""+ref+"\""), ref)
	}

	require.Equal(t, []string{"v1.2.3", "v2.0.0"}, runtimetags.Tags("(forge-ci-runtime-base:v1.2.3), forge-ci-runtime:v2.0.0"))
}
