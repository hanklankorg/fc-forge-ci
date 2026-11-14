// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/gitlab"
)

func TestWebURLs_PointAtTheForgeSpecificPaths(t *testing.T) {
	t.Parallel()

	provider := gitlab.New()
	if got := provider.ReleaseDownloadURLPrefix("https://gitlab.com/", "group/source", "v1.0.0") + "/sbom-app-amd64.json"; got != "https://gitlab.com/group/source/-/releases/v1.0.0/downloads/sbom-app-amd64.json" {
		t.Errorf("release asset URL = %q", got)
	}

	if got := provider.ReleaseDownloadURLPrefix("https://gitlab.com", "group/source", "release/v1"); got != "https://gitlab.com/group/source/-/releases/release%2Fv1/downloads" {
		t.Errorf("escaped release download prefix = %q", got)
	}

	if got := provider.ReleaseWebURL("https://gitlab.com", "owner/repo", "v1.0.0"); got != "https://gitlab.com/owner/repo/-/releases/v1.0.0" {
		t.Errorf("ReleaseWebURL = %q", got)
	}

	if got := provider.PackagesWebURL("https://gitlab.com", "owner/repo"); got != "https://gitlab.com/owner/repo/-/packages" {
		t.Errorf("PackagesWebURL = %q", got)
	}
}
