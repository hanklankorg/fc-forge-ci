// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package kaniko

import (
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// Identity hashes the same executor the adapter invokes without executing it
// or touching the job container's filesystem beyond reading that executable.
func (a *Adapter) Identity() (container.BuilderIdentity, error) {
	digest, err := safeexec.ExecutableSHA256(a.executor())
	if err != nil {
		return container.BuilderIdentity{}, err
	}

	return container.BuilderIdentity{Kind: "kaniko", SHA256: digest}, nil
}
