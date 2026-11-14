// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah

import (
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// Identity hashes the same executable the adapter invokes, without probing
// storage or creating a user namespace.
func (a *Adapter) Identity() (container.BuilderIdentity, error) {
	digest, err := safeexec.ExecutableSHA256(a.bin())
	if err != nil {
		return container.BuilderIdentity{}, err
	}

	return container.BuilderIdentity{Kind: "buildah", SHA256: digest}, nil
}
