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

// LoadRegistryInput copies an already verified OCI layout into native storage.
// No registry is contacted and no registry credential is accepted.
func (a *Adapter) LoadRegistryInput(ctx context.Context, directory, local string, out io.Writer) error {
	id, err := a.outputWithEnv(ctx, nil, a.global("pull", "--quiet", "oci:"+directory+":scan")...)
	if err != nil {
		return err
	}

	id = strings.TrimSpace(id)
	if !container.ValidDigest("sha256:" + strings.TrimPrefix(id, "sha256:")) {
		return fmt.Errorf("local OCI import returned an invalid image ID: %w", errs.ErrValidation)
	}

	return a.run(ctx, out, a.global("tag", id, local)...)
}
