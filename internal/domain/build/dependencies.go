// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"fmt"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"net"
	"net/url"
	"strings"
)

// NPMDependencyAuthConfig authenticates only the named host/path. It deliberately
// sets no registry or scope: the consumer's own npm configuration owns routing.
func NPMDependencyAuthConfig(registry string) (string, error) {
	address, err := url.Parse(registry)
	if err != nil || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" || strings.ContainsAny(address.Path, "\r\n") {
		return "", fmt.Errorf("dependency-registry must be an HTTPS registry URL without credentials, query or fragment: %w", errs.ErrInvalidConfig)
	}

	if !secureDependencyRegistry(address) {
		return "", fmt.Errorf("dependency-registry requires HTTPS (HTTP is only permitted on loopback): %w", errs.ErrInvalidConfig)
	}

	return "//" + address.Host + strings.TrimRight(address.EscapedPath(), "/") + "/:_authToken=${DEPENDENCY_TOKEN}\n", nil
}

func secureDependencyRegistry(address *url.URL) bool {
	ip := net.ParseIP(address.Hostname())
	loopback := address.Hostname() == "localhost" || ip != nil && ip.IsLoopback()

	return address.Scheme == "https" || address.Scheme == "http" && loopback
}
