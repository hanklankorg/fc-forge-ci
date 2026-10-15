// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

var _ provider.ReleaseAssetPruner = (*Provider)(nil)

type gitlabListedRelease struct {
	TagName   string    `json:"tag_name"`
	CreatedAt time.Time `json:"created_at"`
	Assets    struct {
		Links []gitlabReleaseLink `json:"links"`
	} `json:"assets"`
}

// ListReleases lists every release of repo with its asset links, for
// retention. GitLab has no draft releases and reports no link sizes. A
// release's links are listed separately, page by page, so a release with many
// files is seen whole.
func (p *Provider) ListReleases(ctx context.Context, repo string) ([]provider.ListedRelease, error) {
	if _, err := p.ParseRepository(repo); err != nil {
		return nil, err
	}

	apiBase, headers := p.apiContext()
	endpoint := projectEndpoint(apiBase, repo) + "/releases"

	var listed []provider.ListedRelease

	seen := map[string]bool{}

	for number := 1; ; number++ {
		body, err := getJSON(ctx, p.HTTPClient, fmt.Sprintf("%s?per_page=%d&page=%d", endpoint, gitlabPageSize, number), headers)
		if err != nil {
			return nil, fmt.Errorf("gitlab list releases: %w", err)
		}

		var batch []gitlabListedRelease
		if err = json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("gitlab parse releases: %w: %w", err, errs.ErrMalformedInput)
		}

		page, err := p.listedReleasesPage(ctx, endpoint, batch, seen, headers)
		if err != nil {
			return nil, err
		}

		listed = append(listed, page...)

		if len(batch) < gitlabPageSize {
			return listed, nil
		}

		// A server that ignores the page parameter answers every request
		// with the same full page; following it would never end.
		if len(page) == 0 {
			return nil, fmt.Errorf("gitlab list releases: page %d repeats an earlier page: %w", number, errs.ErrMalformedInput)
		}
	}
}

// listedReleasesPage lists the links of each release on one page that was not
// seen on an earlier one. Offset paging shows a release twice when one is
// created while the listing runs.
func (p *Provider) listedReleasesPage(ctx context.Context, endpoint string, batch []gitlabListedRelease, seen map[string]bool,
	headers map[string]string,
) ([]provider.ListedRelease, error) {
	var listed []provider.ListedRelease

	for _, release := range batch {
		if release.TagName == "" || seen[release.TagName] {
			continue
		}

		seen[release.TagName] = true

		links, err := p.listReleaseLinks(ctx, endpoint+"/"+url.PathEscape(release.TagName)+"/assets/links", release.TagName, headers)
		if err != nil {
			return nil, err
		}

		entry := provider.ListedRelease{Tag: release.TagName, Created: release.CreatedAt}
		for _, link := range links {
			entry.Assets = append(entry.Assets, provider.ListedAsset{ID: int64(link.ID), Name: link.Name, URL: link.URL})
		}

		listed = append(listed, entry)
	}

	return listed, nil
}

// DeleteReleaseAsset deletes the project upload the link points at, then the
// link. Deleting only the link would keep the file's storage.
func (p *Provider) DeleteReleaseAsset(ctx context.Context, repo string, release provider.ListedRelease, asset provider.ListedAsset) error {
	if _, err := p.ParseRepository(repo); err != nil {
		return err
	}

	apiBase, headers := p.apiContext()
	project := projectEndpoint(apiBase, repo)
	linksEndpoint := project + "/releases/" + url.PathEscape(release.Tag) + "/assets/links"

	return p.deleteReleaseLink(ctx, project, linksEndpoint, gitlabReleaseLink{ID: int(asset.ID), Name: asset.Name, URL: asset.URL}, headers)
}

// deleteReleaseLink removes a release link together with the project upload
// it points at, the upload first: a link left without its file is visible and
// can be retried, a file left without its link is storage nobody sees. A link
// to anything but an upload of this project, such as a package file, has no
// upload to delete here; only the link goes.
func (p *Provider) deleteReleaseLink(ctx context.Context, project, linksEndpoint string, link gitlabReleaseLink, headers map[string]string) error {
	if err := p.deleteLinkedUpload(ctx, project, link, headers); err != nil {
		return err
	}

	if err := deleteJSON(ctx, p.HTTPClient, linksEndpoint+"/"+strconv.Itoa(link.ID), headers); err != nil {
		return fmt.Errorf("gitlab delete release asset link %q: %w", link.Name, err)
	}

	return nil
}

// deleteLinkedUpload deletes the project upload link points at, if it points
// at one. project is the project's API URL, .../api/v4/projects/<id>.
func (p *Provider) deleteLinkedUpload(ctx context.Context, project string, link gitlabReleaseLink, headers map[string]string) error {
	secret, filename, ok := projectUpload(project, link.URL)
	if !ok {
		return nil
	}

	endpoint := project + "/uploads/" + secret + "/" + url.PathEscape(filename)
	if err := deleteJSON(ctx, p.HTTPClient, endpoint, headers); err != nil {
		return fmt.Errorf("gitlab delete the upload behind release asset link %q: %w", link.Name, err)
	}

	return nil
}

var uploadSecret = regexp.MustCompile(`^[0-9a-f]{32}$`)

// projectUpload reads the secret and file name of a project upload URL on
// the GitLab that serves api, in either form GitLab serves one:
// /<project path>/uploads/<secret>/<file> or /-/project/<id>/uploads/<secret>/<file>.
func projectUpload(api, link string) (string, string, bool) {
	base, err := url.Parse(api)
	if err != nil {
		return "", "", false
	}

	parsed, err := url.Parse(link)
	if err != nil || parsed.Host != base.Host {
		return "", "", false
	}

	_, rest, found := strings.Cut(parsed.Path, "/uploads/")
	if !found {
		return "", "", false
	}

	secret, filename, found := strings.Cut(rest, "/")
	if !found || !uploadSecret.MatchString(secret) || filename == "" || strings.Contains(filename, "/") {
		return "", "", false
	}

	return secret, filename, true
}
