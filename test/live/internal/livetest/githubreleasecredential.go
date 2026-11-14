// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// GitHubReleaseTokenFileEnv names the explicitly role-bound consumer credential,
// never the management/Packages credential in the hosted session context.
const GitHubReleaseTokenFileEnv = "FORGE_CI_LIVE_GITHUB_RELEASE_TOKEN_FILE" //nolint:gosec // Environment key only.

// GitHubReleaseToken reads a private bounded file for RELEASE_TOKEN. Admission
// checks token kind, not provider permissions: the release workflow proves those.
// Diagnostics intentionally contain neither file contents nor caller paths.
func GitHubReleaseToken(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s is required for a consumer release: %w", GitHubReleaseTokenFileEnv, errs.ErrUsage)
	}

	token, err := readTokenFile(path)
	if err != nil {
		return "", fmt.Errorf("%s must name a private regular file containing one bounded token: %w", GitHubReleaseTokenFileEnv, errs.ErrUsage)
	}

	if !strings.HasPrefix(token, "github_pat_") || len(token) <= len("github_pat_") {
		return "", fmt.Errorf("%s requires a fine-grained PAT, not a classic management/Packages token: %w", GitHubReleaseTokenFileEnv, errs.ErrUsage)
	}

	return token, nil
}
