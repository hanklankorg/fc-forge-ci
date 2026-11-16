// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// LoadRegistryInput copies an already verified OCI layout into native storage
// and tags it after its digest. No registry is contacted and no registry
// credential is accepted.
func (a *Adapter) LoadRegistryInput(ctx context.Context, directory, digest string, out io.Writer) (string, error) {
	if !container.ValidDigest(digest) {
		return "", fmt.Errorf("registry input digest %q is not a sha256 digest: %w", digest, errs.ErrValidation)
	}

	id, err := a.outputWithEnv(ctx, nil, a.global("pull", "--quiet", "oci:"+directory+":scan")...)
	if err != nil {
		return "", err
	}

	id = strings.TrimSpace(id)
	if !container.ValidDigest("sha256:" + strings.TrimPrefix(id, "sha256:")) {
		return "", fmt.Errorf("local OCI import returned an invalid image ID: %w", errs.ErrValidation)
	}

	local := container.RegistryInputRepository + ":" + strings.TrimPrefix(digest, "sha256:")

	if err = a.run(ctx, out, a.global("tag", id, local)...); err != nil {
		return "", err
	}

	return local, nil
}

// DiscardRegistryInputs keeps the loaded parents: they are images in the
// store, tagged by digest, which a later build may start from again.
func (a *Adapter) DiscardRegistryInputs() {}
