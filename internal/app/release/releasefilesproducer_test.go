// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"encoding/json"
	"slices"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
)

func TestWriteReleaseFileManifest_FinalizesWithoutBuildToolMetadata(t *testing.T) {
	t.Chdir(t.TempDir())

	for path, body := range map[string]string{
		"dist/app.jar":                            "application\n",
		"dist/app.jar.sig":                        "package signature\n",
		"dist/app.cdx.json":                       "SBOM\n",
		"dist/app.cdx.json.bundle":                "SBOM signature\n",
		"dist/SHA256SUMS.bundle":                  "checksums signature\n",
		"dist/plan.json":                          "evidence\n",
		"dist/plan.json.bundle":                   "evidence signature\n",
		"dist/slsa-provenance.intoto.json":        "provenance\n",
		"dist/slsa-provenance.intoto.json.bundle": "provenance signature\n",
		"dist/unlisted.deb.sig":                   "stray package signature\n",
		"dist/unlisted.sbom.json":                 "stray SBOM\n",
		"dist/unlisted.sbom.json.bundle":          "stray SBOM signature\n",
	} {
		writeReleaseFilesTestFile(t, path, body)
	}

	writeReleaseFilesTestFile(t, "dist/SHA256SUMS",
		testFileSHA256(t, "dist/app.jar")+"  app.jar\n"+
			testFileSHA256(t, "dist/app.cdx.json")+"  app.cdx.json\n")

	checksum := apprelease.FileEntry{Path: "dist/SHA256SUMS", Name: "SHA256SUMS", Source: "producer"}
	sbom := apprelease.FileEntry{Path: "dist/app.cdx.json", Name: "app.cdx.json", Source: "producer"}
	evidence := apprelease.FileEntry{Path: "dist/plan.json", Name: "plan.json", Source: "producer"}
	input := apprelease.FileManifest{
		Version: appreleaseVersionForTest,
		Assets: []apprelease.FileEntry{
			{Path: "dist/app.jar", Name: "app.jar", Source: "producer"}, checksum, sbom, evidence,
		},
		Checksums: []apprelease.FileEntry{checksum},
		SBOMs:     []apprelease.FileEntry{sbom},
		Evidence:  []apprelease.FileEntry{evidence},
	}

	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	writeReleaseFilesTestFile(t, "dist/release-files.json", string(body))

	if validateErr := apprelease.ValidateReleaseChecksums(apprelease.ValidateReleaseChecksumsInput{}); validateErr != nil {
		t.Fatal(validateErr)
	}

	manifest, err := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{})
	if err != nil {
		t.Fatal(err)
	}

	assertReleaseAssetsInclude(t, manifest.Assets,
		"app.jar", "app.jar.sig", "SHA256SUMS", "SHA256SUMS.bundle",
		"app.cdx.json", "app.cdx.json.bundle", "plan.json", "plan.json.bundle",
		"slsa-provenance.intoto.json", "slsa-provenance.intoto.json.bundle")

	if len(manifest.Assets) != 10 || !slices.Equal(manifest.Checksums, input.Checksums) ||
		!slices.Equal(manifest.SBOMs, input.SBOMs) || !slices.Equal(manifest.Evidence, input.Evidence) {
		t.Fatalf("finalization changed the producer's file set or classifications: %+v", manifest)
	}

	if validateErr := apprelease.ValidateReleaseChecksums(apprelease.ValidateReleaseChecksumsInput{}); validateErr != nil {
		t.Fatal(validateErr)
	}

	// A producer's manifest stays authoritative even if unrelated build metadata
	// exists in the directory. Finalization must not parse or discover from it.
	first := readFile(t, "dist/release-files.json")
	writeReleaseFilesTestFile(t, "dist/artifacts.json", "not GoReleaser metadata\n")

	if _, finalizeErr := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{}); finalizeErr != nil {
		t.Fatal(finalizeErr)
	}

	if readFile(t, "dist/release-files.json") != first {
		t.Fatal("repeated finalization changed the producer's publication set")
	}
}
