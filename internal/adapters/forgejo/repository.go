// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import "github.com/diggsweden/forge-ci/internal/domain/repository"

// ParseRepository preserves Forgejo's owner/name spelling, including leading
// punctuation in repository names. It does not apply account-creation rules.
func (*Provider) ParseRepository(raw string) (repository.Path, error) {
	return repository.ParseOwnerName(raw)
}

var _ repository.Parser = (*Provider)(nil)
