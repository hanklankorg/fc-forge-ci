// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github

import (
	"context"
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

var _ provider.ReleaseAssetPruner = (*Provider)(nil)

// ListReleases lists every release of repo, drafts included, with all its
// assets, for retention. A release GitHub marks immutable is reported so, and
// retention keeps it whole.
func (p *Provider) ListReleases(ctx context.Context, repo string) ([]provider.ListedRelease, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	client, err := p.releaseClient(ctx)
	if err != nil {
		return nil, err
	}

	releases, err := listAllRepositoryReleases(ctx, client, owner, name)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", classifyGitHubError(err))
	}

	listed := make([]provider.ListedRelease, 0, len(releases))

	for _, release := range releases {
		if release.GetID() == 0 {
			return nil, fmt.Errorf("list releases: a release without an id: %w", errs.ErrMalformedInput)
		}

		assets, err := listAllReleaseAssets(ctx, client, owner, name, release.GetID())
		if err != nil {
			return nil, fmt.Errorf("list the assets of release %s: %w", release.GetTagName(), classifyGitHubError(err))
		}

		entry := provider.ListedRelease{
			ID: release.GetID(), Tag: release.GetTagName(), Draft: release.GetDraft(), Immutable: release.GetImmutable(),
			Created: release.GetCreatedAt().Time,
		}

		for _, asset := range assets {
			if asset.GetID() == 0 {
				return nil, fmt.Errorf("list the assets of release %s: an asset without an id: %w", release.GetTagName(), errs.ErrMalformedInput)
			}

			entry.Assets = append(entry.Assets, provider.ListedAsset{
				ID: asset.GetID(), Name: asset.GetName(), Size: int64(asset.GetSize()), URL: asset.GetBrowserDownloadURL(),
			})
		}

		listed = append(listed, entry)
	}

	return listed, nil
}

// DeleteReleaseAsset deletes one asset, keeping the release.
func (p *Provider) DeleteReleaseAsset(ctx context.Context, repo string, _ provider.ListedRelease, asset provider.ListedAsset) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}

	client, err := p.releaseClient(ctx)
	if err != nil {
		return err
	}

	if _, err := client.Repositories.DeleteReleaseAsset(ctx, owner, name, asset.ID); err != nil {
		return fmt.Errorf("delete release asset %q: %w", asset.Name, classifyGitHubError(err))
	}

	return nil
}
