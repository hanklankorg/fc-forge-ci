// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package central implements the Sonatype Central Publisher API. Uploads are
// single-attempt; only status observation may be retried by the application.
package central

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const publisherAPI = "https://central.sonatype.com/api/v1/publisher"

// Client carries explicit publisher credentials. BaseURL/HTTP allow controlled
// local HTTP collaborators in tests; the CLI always uses the public API default.
type Client struct {
	Username, Password, BaseURL string
	HTTP                        *http.Client
}

//nolint:gochecknoglobals // deployment IDs are opaque API coordinates, never URLs.
var deploymentID = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

func apiURL(base, endpoint string, query url.Values) (string, error) {
	if base == "" {
		base = publisherAPI
	}

	address, err := url.Parse(base)
	if err != nil || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" {
		return "", fmt.Errorf("invalid Central API origin: %w", errs.ErrInvalidConfig)
	}

	if address.Scheme != "https" && !loopbackHTTP(address) {
		return "", fmt.Errorf("central API requires HTTPS: %w", errs.ErrInvalidConfig)
	}

	address = address.JoinPath(endpoint)
	address.RawQuery = query.Encode()

	return address.String(), nil
}

func loopbackHTTP(address *url.URL) bool {
	addressIP := net.ParseIP(address.Hostname())

	return address.Scheme == "http" && (address.Hostname() == "localhost" || addressIP != nil && addressIP.IsLoopback())
}

func readResponse(response *http.Response) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, fmt.Errorf("invalid or oversized Central response: %w", errs.ErrMalformedInput)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		classification := errs.FromHTTPStatus(response.StatusCode)
		if classification == nil {
			classification = errs.ErrValidation
		}

		return nil, fmt.Errorf("central returned HTTP %d: %w", response.StatusCode, classification)
	}

	return data, nil
}

// Upload streams a multipart bundle once and returns its deployment identity.
func (c Client) Upload(ctx context.Context, filename string, automatic bool) (string, error) {
	file, err := os.Open(filename) // #nosec G304 -- application-created temporary bundle; not a remote path.
	if err != nil {
		return "", err
	}

	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	done := make(chan error, 1)

	go func() {
		defer func() { _ = file.Close() }()

		part, copyErr := form.CreateFormFile("bundle", "publication.zip")
		if copyErr == nil {
			_, copyErr = io.Copy(part, file)
		}

		if copyErr == nil {
			copyErr = form.Close()
		}

		_ = writer.CloseWithError(copyErr)
		done <- copyErr
	}()

	mode := "USER_MANAGED"
	if automatic {
		mode = "AUTOMATIC"
	}

	body, err := c.request(ctx, "upload", url.Values{"publishingType": {mode}}, reader, form.FormDataContentType())
	_ = reader.Close()
	streamErr := <-done

	if err != nil {
		return "", err
	}

	if streamErr != nil {
		return "", fmt.Errorf("central upload stream failed: %w", errs.ErrDependencyUnavailable)
	}

	id := strings.TrimSpace(string(body))
	if !deploymentID.MatchString(id) {
		return "", fmt.Errorf("central returned an invalid deployment identity: %w", errs.ErrMalformedInput)
	}

	return id, nil
}

// State observes the submitted deployment; response bodies never enter errors.
func (c Client) State(ctx context.Context, id string) (string, error) {
	if !deploymentID.MatchString(id) {
		return "", fmt.Errorf("invalid Central deployment identity: %w", errs.ErrUsage)
	}

	body, err := c.request(ctx, "status", url.Values{"id": {id}}, nil, "")
	if err != nil {
		return "", err
	}

	var state struct {
		State string `json:"deploymentState"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return "", fmt.Errorf("invalid Central status response: %w", errs.ErrMalformedInput)
	}

	switch state.State {
	case "PENDING", "VALIDATING", "VALIDATED", "PUBLISHING", "PUBLISHED", "FAILED":
		return state.State, nil
	default:
		return "", fmt.Errorf("unknown Central deployment state: %w", errs.ErrMalformedInput)
	}
}

func (c Client) request(ctx context.Context, endpoint string, query url.Values, body io.Reader, contentType string) ([]byte, error) {
	if c.Username == "" || c.Password == "" {
		return nil, fmt.Errorf("central publisher token username/password are required: %w", errs.ErrPermissionDenied)
	}

	address, err := apiURL(c.BaseURL, endpoint, query)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Password)))

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	client := http.Client{Timeout: 2 * time.Minute}
	if c.HTTP != nil {
		client = *c.HTTP
	}

	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, fmt.Errorf("central API request failed: %w", errs.ErrDependencyUnavailable)
	}

	defer func() { _ = response.Body.Close() }()

	return readResponse(response)
}
