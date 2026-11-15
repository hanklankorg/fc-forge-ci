// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger

import (
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

// PackageNames returns, once each and in ledger order, the package name of
// every entry's image on that registry under that owner: the repository path
// beneath the owner, which is what a forge that keeps packages under the
// owner names the image's package. Images elsewhere are no package of the
// owner's and are left out.
func PackageNames(entries []Entry, registry, owner string) []string {
	var names []string

	for _, entry := range entries {
		ref, err := container.ParseTaggedRef(entry.FinalTag)
		if err != nil || !strings.EqualFold(ref.Host, registry) || !strings.EqualFold(ref.Owner(), owner) {
			continue
		}

		if !slices.Contains(names, ref.Name()) {
			names = append(names, ref.Name())
		}
	}

	return names
}
