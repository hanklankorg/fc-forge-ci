// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package mavenregistry implements artifact-first Maven repository PUTs. Forge
// SDKs resolve coordinates/authentication but do not implement this protocol.
package mavenregistry

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// Client accepts an injected transport for controlled registry integration tests.
type Client struct{ HTTP *http.Client }

// Upload sends one already-validated artifact exactly once. Redirects are
// refused, so registry credentials cannot follow an unrelated authority.
func (c Client) Upload(ctx context.Context, registry provider.ForgeMavenRegistry, name string, body io.Reader, size int64) error {
	address, err := uploadURL(registry.URL, name)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, address, body)
	if err != nil {
		return err
	}

	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")

	if authErr := authenticate(req, registry); authErr != nil {
		return authErr
	}

	client := http.Client{Timeout: 2 * time.Minute}
	if c.HTTP != nil {
		client = *c.HTTP
	}

	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return fmt.Errorf("maven artifact upload failed: %w", errs.ErrDependencyUnavailable)
	}

	defer func() { _ = response.Body.Close() }()

	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		classification := errs.FromHTTPStatus(response.StatusCode)
		if classification == nil {
			classification = errs.ErrValidation
		}

		return fmt.Errorf("maven registry upload returned HTTP %d: %w", response.StatusCode, classification)
	}

	return nil
}

func uploadURL(registry, name string) (string, error) {
	address, err := url.Parse(registry)
	if err != nil || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" {
		return "", fmt.Errorf("invalid Maven registry URL: %w", errs.ErrInvalidConfig)
	}

	if !secureRegistry(address) {
		return "", fmt.Errorf("maven registry requires HTTPS: %w", errs.ErrInvalidConfig)
	}

	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\\r\n") {
		return "", fmt.Errorf("invalid publication path: %w", errs.ErrInvalidConfig)
	}

	// Assign the decoded path so URL.String escapes literal percent signs in
	// artifact filenames instead of interpreting them as encoded separators.
	address.Path = path.Join(address.Path, name)
	address.RawPath = ""

	return address.String(), nil
}

func secureRegistry(address *url.URL) bool {
	ip := net.ParseIP(address.Hostname())
	loopback := address.Hostname() == "localhost" || ip != nil && ip.IsLoopback()

	return address.Scheme == "https" || address.Scheme == "http" && loopback
}

func authenticate(req *http.Request, registry provider.ForgeMavenRegistry) error {
	if registry.Token == "" {
		return fmt.Errorf("maven registry token is required: %w", errs.ErrPermissionDenied)
	}

	switch registry.AuthScheme {
	case provider.MavenAuthServerPassword:
		req.SetBasicAuth(registry.Username, registry.Token)
	case provider.MavenAuthJobTokenHeader:
		req.Header.Set("Job-Token", registry.Token)
	case provider.MavenAuthTokenHeader:
		req.Header.Set("Authorization", "token "+registry.Token)
	default:
		return fmt.Errorf("unsupported Maven authentication scheme: %w", errs.ErrInvalidConfig)
	}

	return nil
}
