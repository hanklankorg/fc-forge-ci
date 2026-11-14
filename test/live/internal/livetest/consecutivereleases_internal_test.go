// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import "testing"

func TestGitLabConsecutiveReleaseSettingsStayOutsideBuildVariables(t *testing.T) {
	t.Parallel()

	settings, err := ParseGitLabReleaseSettings("version=1.2.3\nnext-version=1.2.4\nRELEASE_PLATFORMS=linux/amd64")
	if err != nil || settings.Version != "1.2.3" || settings.NextVersion != "1.2.4" || len(settings.Variables) != 1 {
		t.Fatalf("invalid consecutive release settings: %+v %v", settings, err)
	}

	for _, invalid := range []string{
		"next-version=1.2.4",
		"version=1.2.3\nnext-version=1.2.3",
		"version=1.2.3\nnext-version=1.2.2",
		"version=1.2.3\nnext-version=1.2.4\nnext-version=1.2.5",
	} {
		if _, err := ParseGitLabReleaseSettings(invalid); err == nil {
			t.Fatalf("invalid sequence accepted: %q", invalid)
		}
	}
}
