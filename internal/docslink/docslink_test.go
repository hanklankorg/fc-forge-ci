// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package docslink_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/docslink"
)

func TestRefForPinsReleaseTagsAndFallsBackToMain(t *testing.T) {
	t.Parallel()

	for version, want := range map[string]string{
		"v0.93.2":     "v0.93.2",
		"v1.0.0-rc.1": "v1.0.0-rc.1",
		"dev":         "main",
		"":            "main",
		"v":           "main",
		"version":     "main",
		"v1/../x":     "main",
		"0.93.2":      "main",
	} {
		if got := docslink.RefFor(version); got != want {
			t.Errorf("RefFor(%q) = %q, want %q", version, got, want)
		}
	}
}

func TestOfflineNamesTheDocsCommand(t *testing.T) {
	t.Parallel()

	if got := docslink.Offline("verification.md#swap-policy"); got != "forge-ci docs verification" {
		t.Errorf("Offline = %q", got)
	}
}
