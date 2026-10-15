// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import (
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// TestAuthEnv_ScopesTheEncodedHeaderToItsAudience pins the whole transient
// auth environment. The header is HTTP Basic over "x-access-token:<token>",
// compared against an encoding written out here rather than recomputed, and
// its config key is scoped to the exact remote URL. A runner-injected token
// reaches only the runner's own server: the same credential asked for a
// third-party remote yields the no-prompt guard and nothing else.
func TestAuthEnv_ScopesTheEncodedHeaderToItsAudience(t *testing.T) {
	t.Parallel()

	const (
		ownRemote   = "https://github.com/owner/repo.git"
		otherRemote = "https://codeberg.org/owner/repo.git"
		header      = "GIT_CONFIG_VALUE_0=Authorization: Basic eC1hY2Nlc3MtdG9rZW46Zml4dHVyZS10b2tlbg=="
	)

	ambient := runcontext.Token().Resolve(func(key string) string {
		return map[string]string{
			"GITHUB_ACTIONS": "true", "GITHUB_TOKEN": "fixture-token", "GITHUB_SERVER_URL": "https://github.com",
		}[key]
	})
	gitlabJob := runcontext.Token().Resolve(func(key string) string {
		return map[string]string{
			"GITLAB_CI": "true", "CI_JOB_TOKEN": "fixture-token", "CI_SERVER_URL": "https://gitlab.example",
			"FORGEJO_SERVER_URL": "https://other.example",
		}[key]
	})

	for name, tc := range map[string]struct {
		remote string
		cred   runcontext.Credential
		want   []string
	}{
		"operator token": {
			remote: otherRemote, cred: runcontext.OperatorCredential("fixture-token"),
			want: []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http." + otherRemote + ".extraheader", header},
		},
		"runner token to its own server": {
			remote: ownRemote, cred: ambient,
			want: []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http." + ownRemote + ".extraheader", header},
		},
		"runner token to another forge": {remote: otherRemote, cred: ambient, want: []string{"GIT_TERMINAL_PROMPT=0"}},
		"GitLab job token uses its clone username": {
			remote: "https://gitlab.example/team/private.git", cred: gitlabJob,
			want: []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=1",
				"GIT_CONFIG_KEY_0=http.https://gitlab.example/team/private.git.extraheader",
				"GIT_CONFIG_VALUE_0=Authorization: Basic Z2l0bGFiLWNpLXRva2VuOmZpeHR1cmUtdG9rZW4="},
		},
		"GitLab job token to another forge": {remote: "https://other.example/team/private.git", cred: gitlabJob, want: []string{"GIT_TERMINAL_PROMPT=0"}},
		"ssh remote":                        {remote: "git@github.com:owner/repo.git", cred: ambient, want: []string{"GIT_TERMINAL_PROMPT=0"}},
		"no credential":                     {remote: ownRemote, want: []string{"GIT_TERMINAL_PROMPT=0"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := authEnv(tc.remote, tc.cred); !slices.Equal(got, tc.want) {
				t.Errorf("authEnv =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}
