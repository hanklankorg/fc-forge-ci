// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package toolrecorder_test

import (
	"errors"
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	domainbuild "github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/testutil/toolrecorder"
	"github.com/stretchr/testify/require"
)

// The recorder is only worth having if it satisfies the real ports. These
// assertions fail at compile time when a port changes shape, which is the
// moment to update the recorder rather than when one ecosystem's tests drift.
var (
	_ appbuild.GoTool             = (*toolrecorder.Recorder)(nil)
	_ appbuild.CycloneDXGoModTool = (*toolrecorder.Recorder)(nil)
	_ appbuild.CargoTool          = (*toolrecorder.Recorder)(nil)
	_ appbuild.GradleOps          = (*toolrecorder.Recorder)(nil)
	_ appbuild.MavenOps           = (*toolrecorder.Recorder)(nil)
	_ appbuild.NPMOps             = toolrecorder.NPMView{}
	_ appbuild.NPMRunner          = toolrecorder.NPMView{}
	_ appbuild.XcodeBuildOps      = toolrecorder.XcodeView{}
	_ appbuild.XcodeSecurityOps   = toolrecorder.SecurityView{}
)

func TestRecorder_NormalizesEveryPortIntoOneSequence(t *testing.T) {
	t.Parallel()

	rec := toolrecorder.New(t)
	require.NoError(t, rec.Run(t.Context(), domainbuild.GoRunInput{Dir: "svc", Env: []string{"GOFLAGS=-mod=readonly"}, Args: []string{"build"}}))
	require.NoError(t, rec.RunInherit(t.Context(), nil, nil, "clean", "package"))
	require.NoError(t, rec.RunInDirInherit(t.Context(), "app", nil, nil, "assembleRelease"))
	_, _, err := rec.NPM().Run(t.Context(), "pkg", "ci")
	require.NoError(t, err)
	code, err := rec.Xcode().RunInherit(t.Context(), nil, nil, "archive")
	require.NoError(t, err)
	require.Zero(t, code)

	rec.AssertCalls([]toolrecorder.Call{
		{Dir: "svc", Env: []string{"GOFLAGS=-mod=readonly"}, Args: []string{"build"}},
		{Args: []string{"clean", "package"}},
		{Dir: "app", Args: []string{"assembleRelease"}},
		{Dir: "pkg", Args: []string{"ci"}},
		{Args: []string{"archive"}},
	})
}

func TestRecorder_FailsOneCallAndLeavesTheRestAlone(t *testing.T) {
	t.Parallel()

	stop := errors.New("owned tool refusal") //nolint:err113 // per-test cause.
	rec := toolrecorder.New(t).FailAt(1, stop)

	require.NoError(t, rec.RunInherit(t.Context(), nil, nil, "first"))
	require.ErrorIs(t, rec.RunInherit(t.Context(), nil, nil, "second"), stop)
	require.NoError(t, rec.RunInherit(t.Context(), nil, nil, "third"))
	require.Equal(t, [][]string{{"first"}, {"second"}, {"third"}}, rec.Args())
}
