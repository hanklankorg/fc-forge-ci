// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlab

import "github.com/diggsweden/forge-ci/internal/domain/repository"

// ParseRepository accepts the complete project path, including all subgroups.
// The API boundary URL-escapes this path once; the source spelling is retained.
func (*Provider) ParseRepository(raw string) (repository.Path, error) {
	return repository.Parse(raw)
}

var _ repository.Parser = (*Provider)(nil)
