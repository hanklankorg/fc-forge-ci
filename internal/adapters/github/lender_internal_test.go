// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import "testing"

// On a Forgejo runner $GITHUB_API_URL is Forgejo's API. The lender lives on
// GitHub, so its API root comes from the operator or the GitHub default,
// never from the runner's environment.
func TestLenderAPIBase_IgnoresTheRunnersEnvironment(t *testing.T) {
	t.Setenv("GITHUB_API_URL", "https://forgejo.example/api/v1")

	for _, tc := range []struct{ override, want string }{
		{"", defaultAPIBase},
		{"  https://ghe.example/api/v3/ ", "https://ghe.example/api/v3"},
	} {
		if got := (&Lender{APIBase: tc.override}).apiBase(); got != tc.want {
			t.Errorf("APIBase %q: got %q, want %q", tc.override, got, tc.want)
		}
	}
}
