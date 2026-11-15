// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package imageledger_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
)

// An image is the owner's package by the path beneath the owner, named once
// however many entries it has, whatever the case of the owner's name; an
// image on another registry or under another owner is none.
func TestPackageNames_TakesTheOwnersImagesOnce(t *testing.T) {
	t.Parallel()

	entries := []imageledger.Entry{
		{FinalTag: "forge.example/owner/app:v1.0.0"},
		{FinalTag: "forge.example/owner/app:v1.0.0-alpine"},
		{FinalTag: "forge.example/owner/tools/cli:v1.0.0"},
		{FinalTag: "ghcr.io/owner/elsewhere:v1.0.0"},
		{FinalTag: "forge.example/other/theirs:v1.0.0"},
		{FinalTag: "not a ref"},
	}

	require.Equal(t, []string{"app", "tools/cli"}, imageledger.PackageNames(entries, "forge.example", "Owner"))
	require.Empty(t, imageledger.PackageNames(entries, "forge.example", "nobody"))

	// A registry is its host with the port: the same name on another port
	// is another registry.
	onPort := []imageledger.Entry{{FinalTag: "forge.example:8443/owner/app:v1.0.0"}}
	require.Equal(t, []string{"app"}, imageledger.PackageNames(onPort, "forge.example:8443", "owner"))
	require.Empty(t, imageledger.PackageNames(onPort, "forge.example", "owner"))
}
