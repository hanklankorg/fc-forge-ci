// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
)

// These are policy-owned source gates included after the consumer's file.
// Exercise the renderer's real deep merge, not a replacement-only approximation:
// arrays must be replaced, while unknown mapping entries still survive.
func TestSourceGatesReplaceEarlierSameNamedJobControls(t *testing.T) {
	t.Parallel()

	for _, component := range []string{
		"check", "nanolinter", "megalinter", "build-go", "build-cargo",
		"build-npm", "build-maven", "build-gradle", "build-gradle-android",
	} {
		t.Run(component, func(t *testing.T) {
			t.Parallel()
			body := releaseDocumentBody(t, "templates/"+component+".yml")
			gate, ok := body["$[[ inputs.job-name ]]"].(map[string]any)
			require.True(t, ok)

			merged := map[string]any{
				"rules": []any{map[string]any{"when": "never"}}, "when": "manual", "allow_failure": true,
				"needs": []any{"untrusted-artifacts"}, "cache": []any{map[string]any{"paths": []any{"."}}},
				"before_script": []any{"exit 0"}, "script": []any{"true"}, "after_script": []any{"replace-evidence"},
				"variables": map[string]any{"GIT_STRATEGY": "none", "GIT_CHECKOUT": "false", "UNRELATED_SETTING": "still merged"},
			}
			require.False(t, gitlabrender.RulesAdmit(merged["rules"]))
			gitlabrender.Merge(merged, gate)
			require.Equal(t, []any{map[string]any{"when": "on_success"}}, merged["rules"], "an inherited manual/never rule must not hide the gate")
			require.True(t, gitlabrender.RulesAdmit(merged["rules"]))
			require.Equal(t, false, merged["allow_failure"])
			require.Equal(t, []any{}, merged["needs"], "foreign needs must not become gate prerequisites")
			require.Equal(t, []any{}, merged["cache"], "source gates must not restore an inherited source cache")

			for _, key := range []string{"before_script", "script", "after_script"} {
				require.Equal(t, gate[key], merged[key], key)
			}

			variables := merged["variables"].(map[string]any)
			if component == "check" {
				require.Equal(t, "empty", variables["GIT_STRATEGY"])
				require.Equal(t, "false", variables["GIT_CHECKOUT"], "check performs its own verified checkout")
			} else {
				require.Equal(t, "fetch", variables["GIT_STRATEGY"])
				require.Equal(t, "true", variables["GIT_CHECKOUT"])
			}

			require.Equal(t, "still merged", variables["UNRELATED_SETTING"], "include-mode jobs are not sealed namespaces")
		})
	}
}
