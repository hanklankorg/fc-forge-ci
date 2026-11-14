// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import "path/filepath"

// gitlabGitCliffConfig is the changelog configuration the release component
// reads by default, the copy of templates/gitcliff/ that `forge-ci init`
// writes.
const gitlabGitCliffConfig = ".forge-ci/gitcliff/keepachangelog.toml"

// The git-chglog configurations the harness's fixtures and the demo recipes
// seed in .chglog/: the full changelog's and the commit body's.
const (
	chglogKeepAChangelogConfig = ".chglog/config-keepachangelog.yml"
	chglogMinimalConfig        = ".chglog/config-minimal.yml"
)

// GitLabChangelogInputs are the release policy's renderer inputs the
// consumer's files call for. A consumer that carries the component's default
// configuration gets none, so the component renders with its default,
// git-cliff. Any other consumer, and the harness's own fixtures, which seed
// .chglog/, render with git-chglog from .chglog/.
func GitLabChangelogInputs(consumerRoot string) (map[string]any, error) {
	if consumerRoot != "" {
		cliff, err := exists(filepath.Join(consumerRoot, filepath.FromSlash(gitlabGitCliffConfig)))
		if err != nil {
			return nil, err
		}

		if cliff {
			return map[string]any{}, nil
		}
	}

	return map[string]any{
		"changelog-creator":  "git-chglog",
		"changelog-config":   chglogKeepAChangelogConfig,
		"commit-body-config": chglogMinimalConfig,
	}, nil
}
