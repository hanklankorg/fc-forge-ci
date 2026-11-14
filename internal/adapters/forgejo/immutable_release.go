// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"

	"code.gitea.io/sdk/gitea"
	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

func (p *Provider) matchPublishedRelease(ctx context.Context, client *gitea.Client, owner, repo string, existing *gitea.Release, spec provider.ReleaseSpec, want gitea.CreateReleaseOption) error {
	if existing.TagName != want.TagName || existing.Title != want.Title || existing.Note != want.Note || existing.IsPrerelease != want.IsPrerelease || spec.Draft {
		return fmt.Errorf("published release %s has different metadata; refusing to replace it: %w", spec.Tag, errs.ErrValidation)
	}

	assets, err := listReleaseAttachments(client, owner, repo, existing.ID)
	if err != nil {
		return err
	}

	if len(assets) != len(spec.Assets) {
		return fmt.Errorf("published release %s has a different asset set: %w", spec.Tag, errs.ErrValidation)
	}

	byName := make(map[string]*gitea.Attachment, len(assets))
	for _, asset := range assets {
		if asset == nil || byName[asset.Name] != nil {
			return fmt.Errorf("invalid published asset set: %w", errs.ErrValidation)
		}

		byName[asset.Name] = asset
	}

	for _, file := range spec.Assets {
		asset := byName[filepath.Base(file)]
		if asset == nil {
			return fmt.Errorf("published asset %s is missing: %w", filepath.Base(file), errs.ErrValidation)
		}

		if err := p.matchPublishedAsset(ctx, file, asset.DownloadURL); err != nil {
			return err
		}
	}

	return nil
}

func (p *Provider) matchPublishedAsset(ctx context.Context, path, source string) error {
	server, err := p.serverURL()
	if err != nil {
		return err
	}

	base, err := url.Parse(server)
	if err != nil {
		return err
	}

	remote, err := url.Parse(source)
	if err != nil || remote.User != nil || remote.Scheme != base.Scheme || remote.Host != base.Host {
		return fmt.Errorf("published asset URL must stay on the forge: %w", errs.ErrValidation)
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

	if token := p.credential().For(server); token != "" {
		req.Header.Set("Authorization", "token "+token)
	}

	resp, err := httpretry.SameAuthority(p.httpClient(), "Forgejo").Do(req)
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
