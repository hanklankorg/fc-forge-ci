// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package rawref reads forge state straight from the HTTP API, with no part of
// this repository's provider adapters involved. It is the live tier's oracle:
// the independent observation a scenario checks the product against.
//
// # Why it is its own package
//
// The entire value here is sharing nothing with the thing under test. If the
// oracle called the adapter, an adapter that misread a response would agree
// with itself and the scenario would pass while the forge held something else
// entirely. Living in a separate package lets development/golangci-lint/golangci.yml forbid importing
// internal/adapters/... here, so the independence is a build failure rather
// than a habit that erodes.
//
// It deliberately does not import internal/domain either. The oracle speaks
// wire types, so a change to the product's own vocabulary cannot silently
// reshape what "the forge says" means.
package rawref

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrForge is the sentinel every failure here wraps. The oracle keeps its own
// rather than reusing the product's: depguard forbids importing internal/domain
// precisely so a change there cannot reshape what this package reports.
var ErrForge = errors.New("forge read failed")

// Forge names the API dialect to speak. These are wire-level names kept as
// plain strings so this package depends on nothing of the product's.
const (
	Forgejo = "forgejo"
	GitLab  = "gitlab"
)

// Reader reads one forge instance as one identity.
type Reader struct {
	Forge string
	Base  string // https origin, no trailing slash
	Token string
	Owner string

	// Client overrides the default. TLS trust comes from the system pool,
	// where the lab CA is installed.
	Client *http.Client
}

// Release is what a forge reports about a release, reduced to the fields whose
// equality across forges is the parity claim.
type Release struct {
	Tag    string
	Name   string
	Body   string
	Assets []Asset

	// Draft and Prerelease are the policy state the forge recorded. Forgejo
	// reports both; GitLab has neither native flag, so both stay false there.
	// Its prerelease classification is represented explicitly in the notes.
	Draft      bool
	Prerelease bool
}

// Asset is one attached file. Digest is the SHA-256 of the bytes the forge
// actually serves — the only asset assertion that means anything, since a
// forge can report a name and a size for a file it corrupted.
type Asset struct {
	Name   string
	Size   int64
	Digest string
}

// AssetNames returns the attached names, sorted, for set comparisons.
func (r Release) AssetNames() []string {
	names := make([]string, 0, len(r.Assets))
	for _, asset := range r.Assets {
		names = append(names, asset.Name)
	}

	slices.Sort(names)

	return names
}

// Asset finds an attached asset by name.
func (r Release) Asset(name string) (Asset, bool) {
	for _, asset := range r.Assets {
		if asset.Name == name {
			return asset, true
		}
	}

	return Asset{}, false
}

func (r Reader) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}

	return &http.Client{Timeout: 60 * time.Second}
}

// ReleaseByTag reads a release and downloads every asset to digest it.
// A missing release is (Release{}, false, nil): absence is an answer a scenario
// asserts on, not an error.
func ReleaseByTag(ctx context.Context, reader Reader, repo, tag string) (Release, bool, error) {
	switch reader.Forge {
	case Forgejo:
		return reader.forgejoRelease(ctx, repo, tag)
	case GitLab:
		return reader.gitLabRelease(ctx, repo, tag)
	default:
		return Release{}, false, fmt.Errorf("rawref: no reader for forge %q: %w", reader.Forge, ErrForge)
	}
}

func (r Reader) forgejoRelease(ctx context.Context, repo, tag string) (Release, bool, error) {
	endpoint := r.Base + "/api/v1/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(repo) +
		"/releases/tags/" + url.PathEscape(tag)

	var payload struct {
		TagName    string `json:"tag_name"`
		Name       string `json:"name"`
		Body       string `json:"body"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}

	found, err := r.get(ctx, endpoint, &payload)
	if err != nil || !found {
		return Release{}, false, err
	}

	release := Release{Tag: payload.TagName, Name: payload.Name, Body: payload.Body, Draft: payload.Draft, Prerelease: payload.Prerelease}

	for _, asset := range payload.Assets {
		digest, digestErr := r.digest(ctx, asset.URL)
		if digestErr != nil {
			return Release{}, false, digestErr
		}

		release.Assets = append(release.Assets, Asset{Name: asset.Name, Size: asset.Size, Digest: digest})
	}

	return release, true, nil
}

func (r Reader) gitLabRelease(ctx context.Context, repo, tag string) (Release, bool, error) {
	project := url.PathEscape(r.Owner + "/" + repo)
	endpoint := r.Base + "/api/v4/projects/" + project + "/releases/" + url.PathEscape(tag)

	// GitLab models attachments as links rather than files on the release,
	// so the shapes genuinely differ here. That difference is the
	// adapter's to hide; the oracle just reads each forge as it is.
	var payload struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Assets      struct {
			Links []struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"links"`
		} `json:"assets"`
	}

	found, err := r.get(ctx, endpoint, &payload)
	if err != nil || !found {
		return Release{}, false, err
	}

	release := Release{Tag: payload.TagName, Name: payload.Name, Body: payload.Description}

	for _, link := range payload.Assets.Links {
		source, err := r.gitLabAssetURL(repo, link.URL)
		if err != nil {
			return Release{}, false, err
		}

		digest, size, digestErr := r.digestAndSize(ctx, source)
		if digestErr != nil {
			return Release{}, false, digestErr
		}

		release.Assets = append(release.Assets, Asset{Name: link.Name, Size: size, Digest: digest})
	}

	return release, true, nil
}

// A private upload's browser URL requires a session cookie; PRIVATE-TOKEN
// authenticates the uploads API instead. Resolve only GitLab's two project
// upload URL shapes, using the project and upload identity recorded in the
// release link, never an adapter's interpretation of its own writes.
func (r Reader) gitLabAssetURL(repo, source string) (string, error) {
	remote, err := url.Parse(source)
	if err != nil || !r.allowedURL(remote) {
		return "", fmt.Errorf("rawref: asset URL is outside the forge or points to login: %w", ErrForge)
	}

	project := r.Owner + "/" + repo

	upload, found := strings.CutPrefix(remote.Path, "/"+project+"/uploads/")
	if !found {
		// full_path returned by GitLab uses the numeric project ID.
		path, numeric := strings.CutPrefix(remote.Path, "/-/project/")
		if !numeric {
			return source, nil
		}

		id, rest, uploaded := strings.Cut(path, "/uploads/")
		if !uploaded {
			return source, nil
		}

		if _, parseErr := strconv.ParseUint(id, 10, 64); parseErr != nil {
			return "", fmt.Errorf("rawref: malformed GitLab project upload URL: %w", ErrForge)
		}

		project, upload = id, rest
	}

	secret, filename, found := strings.Cut(upload, "/")
	if decoded, decodeErr := hex.DecodeString(secret); !found || decodeErr != nil || len(decoded) != 16 || filename == "" || strings.Contains(filename, "/") {
		return "", fmt.Errorf("rawref: malformed GitLab upload URL: %w", ErrForge)
	}

	return r.Base + "/api/v4/projects/" + url.PathEscape(project) + "/uploads/" + secret + "/" + url.PathEscape(filename), nil
}

func (r Reader) get(ctx context.Context, endpoint string, into any) (bool, error) {
	resp, err := r.do(ctx, endpoint)
	if err != nil {
		return false, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("rawref: GET %s: HTTP %d: %w", redact(endpoint), resp.StatusCode, ErrForge)
	}

	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return false, fmt.Errorf("rawref: decode %s: %w", redact(endpoint), err)
	}

	return true, nil
}

func (r Reader) digest(ctx context.Context, rawURL string) (string, error) {
	digest, _, err := r.digestAndSize(ctx, rawURL)

	return digest, err
}

// digestAndSize streams an asset and hashes it. The bytes are never held whole:
// an asset is whatever size the scenario attached, and a test helper that can
// be made to allocate it is a denial of service against the developer.
func (r Reader) digestAndSize(ctx context.Context, rawURL string) (string, int64, error) {
	resp, err := r.do(ctx, rawURL)
	if err != nil {
		return "", 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("rawref: download %s: HTTP %d: %w", redact(rawURL), resp.StatusCode, ErrForge)
	}

	hash := sha256.New()

	size, err := io.Copy(hash, resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("rawref: read %s: %w", redact(rawURL), err)
	}

	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func (r Reader) do(ctx context.Context, endpoint string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("rawref: invalid request URL: %w", ErrForge)
	}

	if !r.allowedURL(req.URL) {
		return nil, fmt.Errorf("rawref: request URL is outside the forge or points to login: %w", ErrForge)
	}

	switch r.Forge {
	case Forgejo:
		req.Header.Set("Authorization", "token "+r.Token)
	case GitLab:
		req.Header.Set("PRIVATE-TOKEN", r.Token)
	}

	// PRIVATE-TOKEN is not one of net/http's sensitive headers: the default
	// redirect policy forwards it even to unrelated origins. Clone the client
	// so the oracle's boundary does not mutate a caller's shared client.
	client := *r.client()
	checkRedirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if !r.allowedURL(next.URL) || len(via) >= 10 {
			return ErrForge
		}

		if checkRedirect != nil {
			return checkRedirect(next, via)
		}

		return nil
	}

	resp, err := client.Do(req)
	if err != nil {
		// Client errors can contain a redirect Location's credentials/query.
		return nil, fmt.Errorf("rawref: GET %s failed: %w", redact(endpoint), ErrForge)
	}

	return resp, nil
}

func (r Reader) allowedURL(remote *url.URL) bool {
	base, err := url.Parse(r.Base)
	if err != nil || remote == nil || remote.User != nil || remote.Scheme != base.Scheme || remote.Host != base.Host ||
		(remote.Scheme != "https" && remote.Scheme != "http") || remote.Host == "" {
		return false
	}

	return r.Forge != GitLab || !strings.HasSuffix(remote.Path, "/users/sign_in")
}

// SHA256 digests a local file, so a scenario can state the digest it uploaded
// without recomputing it by hand.
func SHA256(path string) (string, int64, error) {
	file, err := os.Open(path) //nolint:gosec // G304: the scenario's own t.TempDir() file, never input.
	if err != nil {
		return "", 0, fmt.Errorf("rawref: open %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	hash := sha256.New()

	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, fmt.Errorf("rawref: read %s: %w", path, err)
	}

	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

// FormatSize renders a byte count for a failure message.
func FormatSize(n int64) string { return strconv.FormatInt(n, 10) + "B" }

func redact(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "<unparsable endpoint>"
	}

	parsed.RawQuery = ""
	parsed.User = nil
	parsed.Fragment = ""

	return parsed.String()
}
