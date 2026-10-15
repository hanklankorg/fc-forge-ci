// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestReleaseFiles_ChecksumDiscoveryErrorsReachAssetConsumers(t *testing.T) {
	for _, kind := range []string{"ambiguous", "unreadable subtree"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "unreadable subtree" && os.Geteuid() == 0 {
				t.Skip("permission refusal requires an unprivileged process")
			}

			t.Chdir(t.TempDir())
			writeReleaseFilesFixture(t)
			writeReleaseFilesTestFile(t, "dist/extra/other_checksums.txt", "other checksum fixture\n")
			writeReleaseFilesTestFile(t, "dist/extra/other_checksums.txt.bundle", "other bundle fixture\n")

			want := errs.ErrValidation
			message := "found 2"

			if kind == "unreadable subtree" {
				if err := os.Chmod("dist/extra", 0o000); err != nil {
					t.Fatal(err)
				}

				t.Cleanup(func() { _ = os.Chmod("dist/extra", 0o700) }) //nolint:gosec // restore only the owned fixture directory.

				want, message = fs.ErrPermission, "discover checksums"
			}

			assets, err := apprelease.CollectReleaseAssets(apprelease.FilesInput{})
			if assets != nil || !errors.Is(err, want) || !strings.Contains(err.Error(), message) {
				t.Errorf("collect: assets=%v err=%v; want no assets and %v (%s)", assets, err, want, message)
			}

			paths, err := apprelease.ListReleaseFiles(apprelease.ListReleaseFilesInput{Section: "assets"})
			if paths != nil || !errors.Is(err, want) || !strings.Contains(err.Error(), message) {
				t.Errorf("list assets: paths=%v err=%v; want no paths and %v (%s)", paths, err, want, message)
			}

			paths, err = apprelease.PublishableReleaseFiles(apprelease.FilesInput{})
			if paths != nil || !errors.Is(err, want) || !strings.Contains(err.Error(), message) {
				t.Errorf("publish discovery: paths=%v err=%v; want no paths and %v (%s)", paths, err, want, message)
			}
		})
	}
}

func TestReleaseFiles_ChecksumBundlesRemainOptional(t *testing.T) {
	for _, kind := range []string{"signed checksum", "no checksum bundle", "no checksum yet"} {
		t.Run(kind, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeReleaseFilesFixture(t)

			switch kind {
			case "no checksum bundle":
				if err := os.Remove("dist/app_checksums.txt.bundle"); err != nil {
					t.Fatal(err)
				}
			case "no checksum yet":
				removeFixtureChecksum(t)
			}

			assets, err := apprelease.CollectReleaseAssets(apprelease.FilesInput{})
			if err != nil {
				t.Fatal(err)
			}

			assertReleaseAssetsInclude(t, assets.Assets, "app.tar.gz", "app.tar.gz.sbom.json.bundle")

			if got, want := releaseFileEntryNamed(assets.Assets, "app_checksums.txt.bundle"), kind == "signed checksum"; got != want {
				t.Errorf("checksum bundle present=%t, want %t", got, want)
			}

			wantPaths := make([]string, 0, len(assets.Assets))
			for _, asset := range assets.Assets {
				wantPaths = append(wantPaths, asset.Path)
			}

			paths, err := apprelease.ListReleaseFiles(apprelease.ListReleaseFilesInput{Section: "assets"})
			if err != nil || !slices.Equal(paths, wantPaths) {
				t.Errorf("list assets: paths=%v err=%v; want %v", paths, err, wantPaths)
			}

			paths, err = apprelease.PublishableReleaseFiles(apprelease.FilesInput{})
			if err != nil || !slices.Equal(paths, wantPaths) {
				t.Errorf("publish discovery: paths=%v err=%v; want %v", paths, err, wantPaths)
			}

			if kind == "no checksum yet" {
				assertRequiredChecksumStillMissing(t)
			}
		})
	}
}

func removeFixtureChecksum(t *testing.T) {
	t.Helper()

	for _, path := range []string{"dist/app_checksums.txt", "dist/app_checksums.txt.bundle"} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	var artifacts []map[string]string

	if err := json.Unmarshal([]byte(readFile(t, "dist/artifacts.json")), &artifacts); err != nil {
		t.Fatal(err)
	}

	artifacts = slices.DeleteFunc(artifacts, func(artifact map[string]string) bool { return artifact["path"] == "dist/app_checksums.txt" })

	body, err := json.Marshal(artifacts)
	if err != nil {
		t.Fatal(err)
	}

	writeReleaseFilesTestFile(t, "dist/artifacts.json", string(body))
}

func assertRequiredChecksumStillMissing(t *testing.T) {
	t.Helper()

	if _, err := apprelease.FindReleaseChecksumFile(apprelease.FilesInput{}); !errors.Is(err, errs.ErrValidation) {
		t.Errorf("required discovery: got %v, want validation failure", err)
	}

	const canary = "existing output must survive\n"

	writeReleaseFilesTestFile(t, "manifest-output.json", canary)

	manifest, err := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{OutputFile: "manifest-output.json"})
	if manifest != nil || !errors.Is(err, errs.ErrValidation) {
		t.Errorf("manifest without checksum: manifest=%v err=%v; want validation failure", manifest, err)
	}

	if got := readFile(t, "manifest-output.json"); got != canary {
		t.Errorf("failed manifest write changed prior output: %q", got)
	}
}
