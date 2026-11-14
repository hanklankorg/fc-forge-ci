// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// Upload first, then create the release with all links in one API request.
// GitLab has no draft state: a failed upload must not publish a partial release.
func (p *Provider) createImmutableRelease(ctx context.Context, repo string, spec provider.ReleaseSpec) error {
	if err := p.checkCreateRelease(repo, spec); err != nil {
		return err
	}

	apiBase, headers := p.apiContext()
	endpoint := strings.TrimRight(apiBase, "/") + "/api/v4/projects/" + url.PathEscape(repo) + "/releases"

	desc, err := releaseDescription(spec)
	if err != nil {
		return err
	}

	body, err := getJSON(ctx, p.HTTPClient, endpoint+"/"+url.PathEscape(spec.Tag), headers)
	if err == nil {
		return p.verifyPublishedRelease(ctx, endpoint, headers, spec, desc, body)
	}

	if !errors.Is(err, errs.ErrMissingInput) {
		return err
	}

	links := make([]gitlabReleaseLink, 0, len(spec.Assets))
	for _, file := range spec.Assets {
		assetURL, err := p.uploadProjectFile(ctx, apiBase, url.PathEscape(repo), repo, file, headers)
		if err != nil {
			return err
		}

		links = append(links, gitlabReleaseLink{Name: filepath.Base(file), URL: assetURL})
	}

	payload := releasePayload(spec, desc, true)
	payload["assets"] = map[string]any{"links": links}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	headers["Content-Type"] = contentTypeJSON

	return postJSON(ctx, p.HTTPClient, endpoint, headers, encoded)
}

// verifyPublishedRelease accepts an existing release only when its metadata
// and every asset match the spec, so a retry never replaces published bytes.
func (p *Provider) verifyPublishedRelease(ctx context.Context, endpoint string, headers map[string]string, spec provider.ReleaseSpec, desc string, body []byte) error {
	var existing struct {
		Tag         string `json:"tag_name"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &existing); err != nil {
		return err
	}

	if existing.Tag != spec.Tag || existing.Name != cmp.Or(spec.Name, spec.Tag) || existing.Description != desc {
		return fmt.Errorf("published release %s has different metadata; refusing to replace it: %w", spec.Tag, errs.ErrValidation)
	}

	links, err := p.listReleaseLinks(ctx, endpoint+"/"+url.PathEscape(spec.Tag)+"/assets/links", spec.Tag, headers)
	if err != nil {
		return err
	}

	if len(links) != len(spec.Assets) {
		return fmt.Errorf("published release has a different asset set: %w", errs.ErrValidation)
	}

	byName := make(map[string]string, len(links))
	for _, link := range links {
		if _, exists := byName[link.Name]; exists {
			return fmt.Errorf("duplicate published asset: %w", errs.ErrValidation)
		}

		byName[link.Name] = link.URL
	}

	for _, file := range spec.Assets {
		if err := p.matchPublishedAsset(ctx, strings.TrimSuffix(endpoint, "/releases"), headers, file, byName[filepath.Base(file)]); err != nil {
			return err
		}
	}

	return nil
}

// publishedAssetSource returns the URL to read a published asset from. It must
// stay on the forge, and a project upload is read through the uploads API.
func publishedAssetSource(apiBase, source string) (string, error) {
	base, err := url.Parse(apiBase)
	if err != nil {
		return "", err
	}

	remote, err := url.Parse(source)
	if err != nil || remote.User != nil || remote.Scheme != base.Scheme || remote.Host != base.Host {
		return "", fmt.Errorf("published asset URL must stay on the forge: %w", errs.ErrValidation)
	}
	// Private project uploads are authenticated through the uploads API;
	// the browser URL can otherwise redirect a token-bearing request to login.
	if secret, filename, ok := projectUpload(apiBase, source); ok {
		return strings.TrimRight(apiBase, "/") + "/uploads/" + secret + "/" + url.PathEscape(filename), nil
	}

	return source, nil
}

func (p *Provider) matchPublishedAsset(ctx context.Context, apiBase string, headers map[string]string, path, source string) error {
	source, err := publishedAssetSource(apiBase, source)
	if err != nil {
		return err
	}

	local, size, err := pathsafe.HashRegularFile(path)
	if err != nil {
		return err
	}

	// The published asset is bounded by progress, not wall time.
	ctx, transfer := httpretry.StartTransfer(ctx, httpretry.ClientTimeout())
	defer transfer.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := p.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}

	resp, err := httpretry.SameAuthority(client, "GitLab").Do(req)
	if err != nil {
		return transfer.Err(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("read published asset: HTTP %d: %w", resp.StatusCode, errs.ErrValidation)
	}

	digest := sha256.New()

	read, err := io.Copy(digest, io.LimitReader(resp.Body, size+1))
	if err != nil {
		return transfer.Err(err)
	}

	if read != size || !bytes.Equal(digest.Sum(nil), local) {
		return fmt.Errorf("published asset %s differs; refusing to replace it: %w", filepath.Base(path), errs.ErrValidation)
	}

	return nil
}
