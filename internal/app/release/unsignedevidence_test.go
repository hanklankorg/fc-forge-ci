// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"os"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/stretchr/testify/require"
)

func TestUnsignedEvidenceDoesNotImposeNativeImageContractOnExistingLedgers(t *testing.T) {
	for _, kind := range []imageledger.ImageKind{"", imageledger.ImageKindRelease, imageledger.ImageKindBase} {
		t.Run(string(kind), func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeUnsignedReleaseFilesFixture(t)

			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{SBOMBundleNaming: testSBOMBundleNaming})
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

	before, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{SBOMBundleNaming: testSBOMBundleNaming})
	require.NoError(t, err)
	require.NoError(t, os.Remove("dist/artifacts.json"))
	writeReleaseFilesTestFile(t, "dist/plan.json", `{"version":1}`)
	checksums := readFile(t, "dist/checksums.sha256")
	after, err := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{Unsigned: true, EvidenceFiles: []string{"dist/plan.json"}})
	require.NoError(t, err)
	require.Equal(t, before.Checksums, after.Checksums)
	require.Equal(t, before.SBOMs, after.SBOMs)
	require.Equal(t, before.Assets, after.Assets[:len(before.Assets)])
	require.Len(t, after.Assets, len(before.Assets)+1)
	require.Len(t, after.Evidence, 1)
	require.Equal(t, checksums, readFile(t, "dist/checksums.sha256"))
	require.NoError(t, apprelease.ValidateReleaseFileManifest(apprelease.ValidateReleaseFileManifestInput{Unsigned: true}))
}

func TestUnsignedManifestEvidenceRefusalsPreserveOriginal(t *testing.T) {
	for _, scenario := range []string{"missing", "duplicate", "reclassify", "link", "outside", "signed-original", "signed-evidence", "provenance", "without-unsigned", "assets-json", "release-plan"} {
		t.Run(scenario, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeUnsignedReleaseFilesFixture(t)

			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{SBOMBundleNaming: testSBOMBundleNaming})
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
			case "release-plan":
				writeReleaseFilesTestFile(t, "dist/release-plan.json", `{"version":1}`)

				in.EvidenceFiles = []string{"dist/release-plan.json"}
			}

			_, err = apprelease.WriteReleaseFileManifest(in)
			require.Error(t, err)
			require.Equal(t, before, readFile(t, "dist/release-files.json"))
		})
	}
}

// A hand-off whose declaration plans a build SBOM declares at least one;
// one that plans none needs none.
func TestValidateReleaseFileManifestHoldsPlannedBuildSBOMs(t *testing.T) {
	const (
		svc     = "dist/sboms/svc-1.2.3-0123abcd-build-sbom.cyclonedx.json"
		svcCore = "dist/sboms/svc-core-1.2.3-0123abcd-build-sbom.cyclonedx.json"
	)

	for name, test := range map[string]struct {
		sboms          []string
		planned        []string
		containerFirst []string
		want           string
	}{
		"planned and declared": {sboms: []string{"dist/app.cdx.json"}, planned: []string{"app"}},
		"planned, none":        {planned: []string{"app", "lib"}, want: "plans a build SBOM for app, lib"},
		"nothing planned":      {},
		// The build SBOMs forge-ci writes are held by name, beside a build's.
		"container-first, written":       {sboms: []string{"dist/app.cdx.json", svc, svcCore}, planned: []string{"app"}, containerFirst: []string{"svc", "svc-core"}},
		"container-first, none":          {containerFirst: []string{"svc"}, want: "no build SBOM of the container-first svc"},
		"container-first, another's":     {sboms: []string{"dist/app.cdx.json"}, containerFirst: []string{"svc"}, want: "container-first svc:"},
		"container-first, the longer":    {sboms: []string{svcCore}, containerFirst: []string{"svc", "svc-core"}, want: "container-first svc:"},
		"container-first, a built one's": {sboms: []string{svcCore}, planned: []string{"svc-core"}, containerFirst: []string{"svc"}, want: "container-first svc:"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeReleaseFilesTestFile(t, "dist/app.tar.gz", "archive")

			for _, file := range []string{"dist/app.cdx.json", svc, svcCore} {
				writeReleaseFilesTestFile(t, file, `{"bomFormat":"CycloneDX","specVersion":"1.6"}`)
			}

			_, err := apprelease.DeclareReleaseFiles(apprelease.DeclareReleaseFilesInput{SBOMBundleNaming: testSBOMBundleNaming, Assets: []string{"dist/app.tar.gz"}, SBOMs: test.sboms})
			require.NoError(t, err)

			err = apprelease.ValidateReleaseFileManifest(apprelease.ValidateReleaseFileManifestInput{
				Unsigned: true, BuildSBOMArtifacts: test.planned, ContainerFirstBuildSBOMs: test.containerFirst,
			})
			if test.want == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, errs.ErrValidation)
			require.ErrorContains(t, err, test.want)
		})
	}
}
