// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/repository"
)

// ParseRepository preserves GitHub's owner/name spelling for API and Git use.
func (*Provider) ParseRepository(raw string) (repository.Path, error) {
	return repository.ParseOwnerName(raw)
}

var _ repository.Parser = (*Provider)(nil)

// RepositoryAPIBase selects the API for an explicitly addressed GitHub server.
// A runner's API override belongs only to that runner's server; another server
// uses its own GHES API root. github.com uses the separate public API host.
func RepositoryAPIBase(server string, env func(string) string) string {
	runnerServer, runnerAPI := env("GITHUB_SERVER_URL"), env("GITHUB_API_URL")
	server = strings.TrimRight(server, "/")
	key := repositoryServerKey(server)

	if key != "" && key == repositoryServerKey(runnerServer) && runnerAPI != "" {
		return runnerAPI
	}

	if key == defaultServerURL {
		return defaultAPIBase
	}

	return server + "/api/v3"
}

// repositoryServerKey compares HTTP server bases without changing the source
// URL. Host case and numeric/default ports are equivalent; path case is not.
// Escaped paths cannot match a checkout base (checkout preflight refuses them).
func repositoryServerKey(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.RawPath != "" {
		return ""
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)

	defaultPort := map[string]string{"http": "80", "https": "443"}[parsed.Scheme]
	if defaultPort == "" {
		return ""
	}

	if port := parsed.Port(); port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return ""
		}

		parsed.Host = strings.TrimSuffix(parsed.Host, ":"+port)
		if normalized := strconv.FormatUint(number, 10); normalized != defaultPort {
			parsed.Host += ":" + normalized
		}
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/")

	return parsed.String()
}
