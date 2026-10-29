// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"slices"
	"strings"
	"testing"
)

func TestMavenLibraryReleaseInventory(t *testing.T) {
	// Deliberately spell out the producer contract independently of the oracle.
	names := []string{
		"cose-lib-2.7.0.pom", "cose-lib-2.7.0.jar",
		"cose-lib-2.7.0-sources.jar", "cose-lib-2.7.0-javadoc.jar",
		"cose-lib.cdx.json", "cose-lib.cdx.json.bundle",
		"checksums.txt", "checksums.txt.bundle",
		"slsa-provenance.intoto.json", "slsa-provenance.intoto.json.bundle",
	}
	assets := make([]ReleaseAsset, 0, len(names))
	for _, name := range names {
		assets = append(assets, ReleaseAsset{Name: name, URL: "https://forge.invalid/" + name, Size: 1})
	}
	t.Run("exact producer inventory", func(t *testing.T) {
		reversed := slices.Clone(assets)
		slices.Reverse(reversed)
		got, err := MavenLibraryReleaseInventory(reversed, "cose-lib", "2.7.0")
		if err != nil {
			t.Fatal(err)
		}
		for _, asset := range assets {
			if got[asset.Name] != asset.URL {
				t.Errorf("wrong URL for %s: %q", asset.Name, got[asset.Name])
			}
		}
	})
	for _, name := range []string{
		"unexpected.zip", "settings.xml", "settings.xml.bundle",
		"other.cdx.json", "other.cdx.json.bundle", "checksums.sha256",
		"release-files.json", "release-notes.md", "cose-lib-2.7.0-sboms.zip",
		"cose-lib-2.7.0.jar.sig", "cose-lib-2.7.0.jar.bundle",
		"checksums.txt.bundle.bundle", "dist/checksums.txt", "../settings.xml",
		"cose-lib-2.7.1.jar",
	} {
		t.Run("unexpected/"+name, func(t *testing.T) {
			bad := append(slices.Clone(assets), ReleaseAsset{Name: name, URL: "https://forge.invalid/extra", Size: 1})
			if _, err := MavenLibraryReleaseInventory(bad, "cose-lib", "2.7.0"); err == nil || !strings.Contains(err.Error(), "unexpected") {
				t.Fatalf("expected unexpected-asset rejection, got %v", err)
			}
		})
	}
	for index, asset := range assets {
		t.Run("missing/"+asset.Name, func(t *testing.T) {
			bad := slices.Delete(slices.Clone(assets), index, index+1)
			if _, err := MavenLibraryReleaseInventory(bad, "cose-lib", "2.7.0"); err == nil || !strings.Contains(err.Error(), "missing") {
				t.Fatalf("expected missing-asset rejection, got %v", err)
			}
		})
		t.Run("duplicate/"+asset.Name, func(t *testing.T) {
			duplicate := asset
			duplicate.URL = "https://forge.invalid/replacement"
			bad := append(slices.Clone(assets), duplicate)
			if _, err := MavenLibraryReleaseInventory(bad, "cose-lib", "2.7.0"); err == nil || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("expected duplicate-asset rejection, got %v", err)
			}
		})
	}
	for _, asset := range []ReleaseAsset{
		{Name: names[0], URL: assets[0].URL, Size: 0},
		{Name: names[0], URL: assets[0].URL, Size: -1},
		{Name: names[0], Size: 1},
	} {
		bad := slices.Clone(assets)
		bad[0] = asset
		if _, err := MavenLibraryReleaseInventory(bad, "cose-lib", "2.7.0"); err == nil {
			t.Errorf("accepted invalid asset %+v", asset)
		}
	}
}
