// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package httpretry

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

// DefaultClientTimeout is the per-request HTTP timeout API clients apply
// when FORGE_CI_HTTP_TIMEOUT is unset. A client from NewClient caps total
// wall time for one ordinary request with it, retries and response body
// included; the transport's MaxCumulativeDelay caps in-process backoff sleep
// separately. A request whose body is a file runs under a Transfer instead,
// which uses the same value as its idle window: it ends only once that long
// passes with no byte moved.
const DefaultClientTimeout = 30 * time.Second

// timeoutEnv overrides DefaultClientTimeout with a Go duration string.
const timeoutEnv = "FORGE_CI_HTTP_TIMEOUT"

// ClientTimeout resolves the per-request HTTP timeout for API clients from
// FORGE_CI_HTTP_TIMEOUT; see ParseClientTimeout for the rules.
func ClientTimeout() time.Duration {
	return ParseClientTimeout(os.Getenv(timeoutEnv))
}

// ParseClientTimeout turns a FORGE_CI_HTTP_TIMEOUT value into a timeout: a
// Go duration ("45s", "2m") when set and positive, otherwise
// DefaultClientTimeout. A malformed or non-positive override is ignored with a
// warning rather than failing the run — the default is always safe, and a slow
// link should not be fatal. clig.dev §Robustness asks to allow network
// timeouts to be configured with a reasonable default so it does not hang
// forever.
func ParseClientTimeout(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultClientTimeout
	}

	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("ignoring invalid "+timeoutEnv+"; using default",
			"value", raw, "default", DefaultClientTimeout.String())

		return DefaultClientTimeout
	}

	return d
}
