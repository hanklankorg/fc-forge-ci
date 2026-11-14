// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"strings"
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/stretchr/testify/require"
)

// Every value the archive step accepts becomes an xcodebuild argument. Whether
// a refusal happens BEFORE the tool runs is pinned by
// TestXcodeReleaseBuild_PreflightRefusalsLeaveAllChannelsUntouched.
//
// The question here is whether a value survives as ONE argument. A scheme called
// "My App" is ordinary on macOS, and the difference between passing it as one
// element and as two is the difference between building it and building
// something else — or nothing. Go's exec does not split on spaces, so this
// holds by construction, and that is exactly why it is worth a test: the day
// someone builds the argv by joining strings, nothing else would notice.

// A value containing spaces or shell metacharacters must arrive as one argv
// element, unquoted and unsplit. Nothing here goes through a shell, so the
// metacharacters are ordinary bytes — and a test that says so is what would
// catch a future argv built by string concatenation.
// No t.Parallel: xcodeReleaseFixture uses t.Setenv and t.Chdir.
func TestXcodeReleaseBuild_AwkwardValuesSurviveAsSingleArguments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scheme string
		config string
		why    string
	}{
		{name: "spaces", scheme: "My App", config: "Release Candidate", why: "ordinary on macOS"},
		{name: "shell metacharacters", scheme: "App;rm -rf /", config: "Rel$(whoami)", why: "no shell is involved, so these are just bytes"},
		{name: "quotes", scheme: `App "Prod"`, config: "Rel'ease", why: "quoting is a shell concept, not an exec one"},
		{name: "a leading dash", scheme: "-App", config: "-Release", why: "xcodebuild reads it as a value because it follows its flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys, in := xcodeReleaseFixture(t)
			in.EnableCodeSigning = false
			in.Scheme, in.Configuration = tc.scheme, tc.config

			var seen []string

			ops := &fakeXcodeBuild{run: func(args []string) {
				seen = append([]string(nil), args...)

				fsys.MkdirAll("checkout/build/app.xcarchive")
			}}

			var out, stderr strings.Builder

			require.NoError(t, appbuild.XcodeReleaseBuild(t.Context(), fakeoutputsink.New(t), unsignedXcodeSecurity{t}, ops,
				output.Annotator{}, &out, &stderr, in))

			require.Equalf(t, tc.scheme, argAfter(t, seen, "-scheme"),
				"the scheme did not survive as one argument: %s", tc.why)
			require.Equalf(t, tc.config, argAfter(t, seen, "-configuration"),
				"the configuration did not survive as one argument: %s", tc.why)
		})
	}
}

func argAfter(t *testing.T, args []string, flag string) string {
	t.Helper()

	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}

	t.Fatalf("%s not found in %q", flag, args)

	return ""
}
