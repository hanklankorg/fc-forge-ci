// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"os"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/stretchr/testify/require"
)

func TestUnsignedEvidenceDoesNotImposeNativeImageContractOnExistingLedgers(t *testing.T) {
	for _, kind := range []imageledger.ImageKind{"", imageledger.ImageKindRelease, imageledger.ImageKindBase} {
		t.Run(string(kind), func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeUnsignedReleaseFilesFixture(t)
			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{})
			require.NoError(t, err)
			digest := "sha256:" + strings.Repeat("a", 64)
			entry := imageledger.Entry{ImageKind: kind, Role: "app", Ref: "registry.example/org/app@" + digest, Digest: digest, FinalTag: "registry.example/org/app:v1.2.3", SBOM: "dist/image-sbom-app.cyclonedx.json"}
			if kind == imageledger.ImageKindBase {
				entry.Flavor, entry.BaseInputID = "core", strings.Repeat("b", 64)
				entry.FinalTag = "registry.example/org/app:" + entry.BaseInputID + "-core"
				entry.SBOM = "premade-sboms/base-sbom-core.cyclonedx.json"
			}
			body, err := imageledger.Marshal([]imageledger.Entry{entry})
			require.NoError(t, err)
			_, err = imageledger.Parse(body)
			require.NoError(t, err, "the compatibility case must carry a valid ledger")
			require.NoError(t, os.WriteFile("dist/release-images.json", body, 0o600))
			writeReleaseFilesTestFile(t, "dist/plan.json", "legacy evidence\n")
			_, err = apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{Unsigned: true, EvidenceFiles: []string{"dist/plan.json"}})
			require.NoError(t, err, "legacy and base workflows do not opt into the native flat-dist SBOM contract")
		})
	}
}

func TestUnsignedManifestExtendsProducerEvidenceWithoutRediscovery(t *testing.T) {
	t.Chdir(t.TempDir())
	writeUnsignedReleaseFilesFixture(t)

	before, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{})
	require.NoError(t, err)
	require.NoError(t, os.Remove("dist/artifacts.json"))
	writeReleaseFilesTestFile(t, "dist/plan.json", `{"version":1}`)
	checksums := readFile(t, "dist/app_checksums.txt")
	after, err := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{Unsigned: true, EvidenceFiles: []string{"dist/plan.json"}})
	require.NoError(t, err)
	require.Equal(t, before.Checksums, after.Checksums)
	require.Equal(t, before.SBOMs, after.SBOMs)
	require.Equal(t, before.Assets, after.Assets[:len(before.Assets)])
	require.Len(t, after.Assets, len(before.Assets)+1)
	require.Len(t, after.Evidence, 1)
	require.Equal(t, checksums, readFile(t, "dist/app_checksums.txt"))
	require.NoError(t, apprelease.ValidateReleaseFileManifest(apprelease.ValidateReleaseFileManifestInput{Unsigned: true}))
}

func TestUnsignedManifestEvidenceRefusalsPreserveOriginal(t *testing.T) {
	for _, scenario := range []string{"missing", "duplicate", "reclassify", "link", "outside", "signed-original", "signed-evidence", "provenance", "without-unsigned", "assets-json"} {
		t.Run(scenario, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeUnsignedReleaseFilesFixture(t)

			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{})
			require.NoError(t, err)
			before := readFile(t, "dist/release-files.json")
			writeReleaseFilesTestFile(t, "dist/plan.json", "evidence\n")

			in := apprelease.WriteReleaseFileManifestInput{Unsigned: true, EvidenceFiles: []string{"dist/plan.json"}}

			switch scenario {
			case "missing":
				in.EvidenceFiles = []string{"dist/missing.json"}
			case "duplicate":
				in.EvidenceFiles = append(in.EvidenceFiles, "dist/plan.json")
			case "reclassify":
				in.EvidenceFiles = []string{"dist/app.tar.gz"}
			case "link":
				require.NoError(t, os.Symlink("plan.json", "dist/link.json"))

				in.EvidenceFiles = []string{"dist/link.json"}
			case "outside":
				writeReleaseFilesTestFile(t, "outside.json", "outside\n")

				in.EvidenceFiles = []string{"outside.json"}
			case "signed-original":
				writeReleaseFilesTestFile(t, "dist/app.tar.gz.bundle", "signature\n")
			case "signed-evidence":
				writeReleaseFilesTestFile(t, "dist/plan.json.bundle", "signature\n")
			case "provenance":
				writeReleaseFilesTestFile(t, "dist/slsa-provenance.intoto.json", "provenance\n")
			case "without-unsigned":
				in.Unsigned = false
			case "assets-json":
				in.AssetsJSONFile = "-"
			}

			_, err = apprelease.WriteReleaseFileManifest(in)
			require.Error(t, err)
			require.Equal(t, before, readFile(t, "dist/release-files.json"))
		})
	}
}
