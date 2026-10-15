// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github_test

import (
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/github"
)

func TestRepositoryAPIBaseBindsToTheSelectedServer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ server, runnerServer, runnerAPI, want string }{
		{"https://github.com", "", "", "https://api.github.com"},
		{"https://GitHub.com:443/", "", "", "https://api.github.com"},
		{"https://GitHub.com:0443/", "", "", "https://api.github.com"},
		{"https://ghe.example", "", "", "https://ghe.example/api/v3"},
		{"https://ghe.example/prefix/", "", "", "https://ghe.example/prefix/api/v3"},
		{"https://ghe.example", "https://ghe.example/", "https://api.ghe.example/custom", "https://api.ghe.example/custom"},
		{"https://GHE.example:443/prefix/", "https://ghe.example/prefix", "https://api.ghe.example/custom", "https://api.ghe.example/custom"},
		{"http://GHE.example:80/prefix", "http://ghe.example/prefix/", "http://api.ghe.example/custom", "http://api.ghe.example/custom"},
		{"https://GHE.example:08443/", "https://ghe.example:8443", "https://api.ghe.example/custom", "https://api.ghe.example/custom"},
		{"https://ghe.example/prefix", "https://ghe.example/Prefix", "https://api.ghe.example/custom", "https://ghe.example/prefix/api/v3"},
		{"https://ghe.example:8443/prefix", "https://ghe.example/prefix", "https://api.ghe.example/custom", "https://ghe.example:8443/prefix/api/v3"},
		{"https://ghe.example/prefix", "http://ghe.example/prefix", "https://api.ghe.example/custom", "https://ghe.example/prefix/api/v3"},
		{"https://ghe.example/prefix", "https://ghe.example/prefix?other=1", "https://api.ghe.example/custom", "https://ghe.example/prefix/api/v3"},
		{"https://ghe.example/prefix", "https://ghe.example/prefix%2F", "https://api.ghe.example/custom", "https://ghe.example/prefix/api/v3"},
		{"https://other.example", "https://ghe.example", "https://api.ghe.example/custom", "https://other.example/api/v3"},
	} {
		t.Run(tc.server+"/"+tc.runnerAPI, func(t *testing.T) {
			t.Parallel()

			env := func(key string) string {
				return map[string]string{"GITHUB_SERVER_URL": tc.runnerServer, "GITHUB_API_URL": tc.runnerAPI}[key]
			}
			if got := github.RepositoryAPIBase(tc.server, env); got != tc.want {
				t.Fatalf("API base=%q, want %q", got, tc.want)
			}
		})
	}
}
