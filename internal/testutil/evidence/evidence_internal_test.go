// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package evidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The refusal to read an unrecognised profile as the default is the part of
// this package most likely to be "simplified" later, so it is pinned first:
// FORGE_CI_TEST_PROFILE=Strict silently meaning "skip freely" would turn a
// gate off with a typo and report success either way.
func TestParseProfile_RefusesAnythingButEmptyOrStrict(t *testing.T) {
	t.Parallel()

	for _, accepted := range []struct {
		value  string
		strict bool
	}{
		{value: "", strict: false},
		{value: "   ", strict: false},
		{value: StrictProfile, strict: true},
		{value: "  strict\n", strict: true},
	} {
		strict, err := parseProfile(accepted.value)
		require.NoErrorf(t, err, "profile %q", accepted.value)
		require.Equalf(t, accepted.strict, strict, "profile %q", accepted.value)
	}

	for _, rejected := range []string{"Strict", "STRICT", "1", "true", "yes", "default", "lenient"} {
		_, err := parseProfile(rejected)
		require.Errorf(t, err, "profile %q was accepted", rejected)
		require.ErrorIs(t, err, errProfile)
		require.Containsf(t, err.Error(), ProfileEnv, "refusal does not name the variable: %v", err)
		require.Containsf(t, err.Error(), StrictProfile, "refusal does not name the accepted value: %v", err)
	}
}

func TestSkipRefusal_OnlyTheStrictProfileForbidsSkipping(t *testing.T) {
	t.Parallel()

	require.NoError(t, skipRefusal("", "buildah is not on PATH"))

	err := skipRefusal(StrictProfile, "buildah is not on PATH")
	require.ErrorIs(t, err, errNotProven)
	require.Contains(t, err.Error(), "buildah is not on PATH")

	require.ErrorIs(t, skipRefusal("nonsense", "buildah is not on PATH"), errProfile)
}

func TestTally_RendersOneLinePerCauseWithCounts(t *testing.T) {
	t.Parallel()

	counted := tally{counts: map[string]int{}}
	require.Empty(t, counted.render(), "a run that proved everything should say nothing")

	counted.record("trivy is not on PATH")
	counted.record("trivy is not on PATH")
	counted.record("buildah is not on PATH")

	rendered := counted.render()
	require.Contains(t, rendered, "3 test(s) skipped for 2 unproven cause(s)")
	require.Contains(t, rendered, "trivy is not on PATH (2 test(s))")
	require.Contains(t, rendered, "buildah is not on PATH (1 test(s))")
	require.Less(t, strings.Index(rendered, "buildah"), strings.Index(rendered, "trivy"),
		"causes are sorted so two runs of the same gap compare as text")
}

// Stderr alone would not do: `go test` discards what a passing package wrote
// unless -v is given, which is precisely the run whose gaps go unnoticed. The
// file is what the justfile prints afterwards.
func TestReport_AppendsToTheFileTheRunnerAsked(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "not-proven.txt")

	var stderr strings.Builder

	require.NoError(t, report(&stderr, path, ""))
	require.Empty(t, stderr.String())
	require.NoFileExists(t, path, "an empty summary must not create a report")

	require.NoError(t, report(&stderr, path, "buildah.test: 1 test(s) skipped\n"))
	require.NoError(t, report(&stderr, path, "trivy.test: 2 test(s) skipped\n"))

	require.Contains(t, stderr.String(), "NOT PROVEN: buildah.test")

	written, err := os.ReadFile(path) //nolint:gosec // test-owned temporary path.
	require.NoError(t, err)
	require.Equal(t,
		"NOT PROVEN: buildah.test: 1 test(s) skipped\nNOT PROVEN: trivy.test: 2 test(s) skipped\n",
		string(written),
		"each binary appends; none overwrites another's record")
}

func TestRequireTool_ReturnsThePathOfAToolThatIsPresent(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "evidence-fixture-tool")
	require.NoError(t, os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o700)) //nolint:gosec // an executable is the point of the fixture.
	t.Setenv("PATH", dir)

	before := tallySnapshot()

	require.Equal(t, tool, RequireTool(t, "evidence-fixture-tool"))
	require.Equal(t, before, tallySnapshot(), "a tool that is present is not a shortfall")
}

// The skip path cannot be asserted from the outside -- t.Skip unwinds the
// goroutine -- so it is observed from a subtest's cleanup, which still runs.
func TestRequireTool_SkipsAndTalliesWhenTheToolIsAbsent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	useProfile(t, "")

	before := tallySnapshot()["evidence-absent-tool is not on PATH"]

	t.Run("absent", func(sub *testing.T) {
		sub.Cleanup(func() {
			if !sub.Skipped() {
				sub.Error("RequireTool returned for a tool that is not on PATH")
			}
		})

		RequireTool(sub, "evidence-absent-tool")
	})

	require.Equal(t, before+1, tallySnapshot()["evidence-absent-tool is not on PATH"],
		"the skip was taken but nothing recorded it, which is the hole this package closes")
}

// The tests that most need this accounting isolate their environment with
// internal/testutil/isolatedenv, which clears every FORGE_CI_ variable --
// the profile among them. Read at skip time, a strict run would quietly
// become a permissive one inside exactly those tests. So the profile is the
// one captured when the binary loaded, and the environment a test presents
// is not consulted: here a "strict" value that arrived afterwards must not
// turn this skip into a failure.
func TestNotProven_UsesTheProfileCapturedAtLoadNotTheTestEnvironment(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	useProfile(t, "")
	t.Setenv(ProfileEnv, StrictProfile)

	t.Run("absent", func(sub *testing.T) {
		sub.Cleanup(func() {
			if !sub.Skipped() {
				sub.Error("the profile was read from the test's environment rather than from the run")
			}
		})

		RequireTool(sub, "evidence-absent-tool")
	})
}

// useProfile pins the run profile for one test and restores it afterwards, so
// the package's own tests behave the same under `just test-integration` and
// `just test-integration-strict`.
func useProfile(t *testing.T, value string) {
	t.Helper()

	previous := runProfile

	t.Cleanup(func() { runProfile = previous })

	runProfile = value
}

// tallySnapshot copies the package tally so a test can assert on what a call
// added without clearing what the rest of the binary recorded.
func tallySnapshot() map[string]int {
	notProven.mu.Lock()
	defer notProven.mu.Unlock()

	copied := make(map[string]int, len(notProven.counts))
	for reason, count := range notProven.counts {
		copied[reason] = count
	}

	return copied
}
