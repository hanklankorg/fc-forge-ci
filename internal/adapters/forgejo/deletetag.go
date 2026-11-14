// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// DeleteTag removes a container tag from the Forgejo registry via the
// package API (Gitea SDK DeletePackage), keeping the manifest. This is
// the only SAFE delete for the shared-digest promotion model: staging
// and final tags share one manifest (promotion is a digest-preserving
// retag), so a manifest-level delete (skopeo) would destroy the promoted
// release image. The package API deletes the tag's package *version*,
// leaving the manifest reachable by the final tag.
//
// ref is a full image ref, e.g. "forge.example/owner/repo:staging-v1.2.3". The
// split is container.ParseTaggedRef — shared with the other forge adapters so
// the digest-pinned refusal is stated once — and the parts are resolved against
// the configured Forgejo server. The package API knows packages only by owner
// and name, so a ref on another host (GHCR, say) is refused before the call:
// resolved here, it would delete a same-named package's tag on this server.
// Implements provider.TagDeleter and imageledger.TagDeleter.
func (p *Provider) DeleteTag(ctx context.Context, ref string) error {
	parsed, err := container.ParseTaggedRef(ref)
	if err != nil {
		return err
	}

	server, err := p.serverURL()
	if err != nil {
		return err
	}

	if serverURL, parseErr := url.Parse(server); parseErr != nil || !strings.EqualFold(serverURL.Host, parsed.Host) {
		return fmt.Errorf("forgejo delete container tag %q: the image is not in the registry of %s,"+
			" whose package API would delete another package's tag: %w", ref, server, errs.ErrUsage)
	}

	client, err := p.client(ctx)
	if err != nil {
		return err
	}

	if resp, err := client.DeletePackage(parsed.Owner(), "container", parsed.Name(), parsed.Tag); err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil
		}

		return fmt.Errorf("forgejo delete container tag %q: %w", ref, classifyErr(resp, err))
	}

	return nil
}
