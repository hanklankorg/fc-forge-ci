// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"context"
	"fmt"

	"code.gitea.io/sdk/gitea"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

var _ provider.ReleaseAssetPruner = (*Provider)(nil)

// ListReleases lists every release of repo with all its attachments, for
// retention. The attachments are listed per release, page by page, so a
// release with many files is seen whole.
func (p *Provider) ListReleases(ctx context.Context, repo string) ([]provider.ListedRelease, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	client, err := p.client(ctx)
	if err != nil {
		return nil, err
	}

	releases, err := listReleases(client, owner, name)
	if err != nil {
		return nil, err
	}

	listed := make([]provider.ListedRelease, 0, len(releases))

	for _, release := range releases {
		entry, err := listedRelease(client, owner, name, release)
		if err != nil {
			return nil, err
		}

		listed = append(listed, entry)
	}

	return listed, nil
}

// listedRelease is one release with every attachment, listed page by page.
func listedRelease(client *gitea.Client, owner, repo string, release *gitea.Release) (provider.ListedRelease, error) {
	if release == nil || release.ID <= 0 {
		return provider.ListedRelease{}, fmt.Errorf("forgejo list releases: a release without an id: %w", errs.ErrMalformedInput)
	}

	attachments, err := listReleaseAttachments(client, owner, repo, release.ID)
	if err != nil {
		return provider.ListedRelease{}, err
	}

	entry := provider.ListedRelease{ID: release.ID, Tag: release.TagName, Draft: release.IsDraft, Created: release.CreatedAt}

	for _, attachment := range attachments {
		if attachment == nil || attachment.ID <= 0 {
			return provider.ListedRelease{}, fmt.Errorf("forgejo list release assets of %s: an asset without an id: %w", release.TagName, errs.ErrMalformedInput)
		}

		entry.Assets = append(entry.Assets, provider.ListedAsset{
			ID: attachment.ID, Name: attachment.Name, Size: attachment.Size, URL: attachment.DownloadURL,
		})
	}

	return entry, nil
}

// DeleteReleaseAsset deletes one attachment, keeping the release.
func (p *Provider) DeleteReleaseAsset(ctx context.Context, repo string, release provider.ListedRelease, asset provider.ListedAsset) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	client, err := p.client(ctx)
	if err != nil {
		return err
	}

	return deleteReleaseAttachment(client, owner, name, release.ID, &gitea.Attachment{ID: asset.ID, Name: asset.Name}, "retired")
}

func listReleases(client *gitea.Client, owner, repo string) ([]*gitea.Release, error) {
	opt := gitea.ListReleasesOptions{ListOptions: gitea.ListOptions{Page: 1, PageSize: 50}}

	var all []*gitea.Release

	for {
		releases, resp, err := client.ListReleases(owner, repo, opt)
		if err != nil {
			return nil, fmt.Errorf("forgejo list releases: %w", classifyErr(resp, err))
		}

		all = append(all, releases...)

		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}

		// A server repeating its Link header would otherwise be followed
		// until the job timed out.
		if resp.NextPage <= opt.Page {
			return nil, fmt.Errorf("forgejo list releases: next page %d does not advance past page %d: %w",
				resp.NextPage, opt.Page, errs.ErrMalformedInput)
		}

		opt.Page = resp.NextPage
	}
}
