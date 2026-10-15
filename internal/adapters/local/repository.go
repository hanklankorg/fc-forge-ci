// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package local

import "github.com/diggsweden/forge-ci/internal/domain/repository"

// ParseRepository checks structural safety without assuming a remote forge.
func (*Provider) ParseRepository(raw string) (repository.Path, error) {
	return repository.Parse(raw)
}

var _ repository.Parser = (*Provider)(nil)
