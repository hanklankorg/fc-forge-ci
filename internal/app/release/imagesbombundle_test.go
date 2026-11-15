// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/archive"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/sbom"
)

const handoffBundle = "dist/app-1.2.3-sboms.tar.gz"

// writeIndexSBOM writes a two-platform index SBOM and its inventories into
// dist, named as the engine names a release image's SBOMs, and returns the
// pinned ledger entry of the image they describe.
func writeIndexSBOM(t *testing.T, role string) imageledger.Entry {
	t.Helper()

	repository := "registry.example/org/" + role
	parts := make([]sbom.IndexBOMPart, 0, 2)
	children := make([]any, 0, 2)

	for index, arch := range []string{"amd64", "arm64"} {
		digest := "sha256:" + strings.Repeat(strconv.Itoa(index+1), 64)
		name := role + "-1.2.3-abcdef0-analyzed-container-linux-" + arch + "-sbom.cyclonedx.json"
		body := fmt.Appendf(nil, `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library","name":"%s-%s"}]}`, role, arch)
		writeReleaseFilesTestFile(t, "dist/"+name, string(body))

		parts = append(parts, sbom.IndexBOMPart{Platform: "linux/" + arch, ImageRef: repository + "@" + digest, Name: name, SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Document: body})
		children = append(children, map[string]any{"digest": digest, "platform": map[string]string{"os": "linux", "architecture": arch}})
	}

	index, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": children})
	require.NoError(t, err)

	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(index))
	entry := imageledger.Entry{Role: role, Ref: repository + "@" + digest, Digest: digest, FinalTag: repository + ":v1.2.3", SBOM: "dist/" + role + "-1.2.3-abcdef0-analyzed-container-index-sbom.cyclonedx.json"}

	body, err := sbom.ComposeIndexBOM(entry.Ref, index, parts)
	require.NoError(t, err)
	writeReleaseFilesTestFile(t, entry.SBOM, string(body))

	entry.SBOMSHA256 = fmt.Sprintf("%x", sha256.Sum256(body))

	return entry
}

func writeImageLedger(t *testing.T, entries ...imageledger.Entry) {
	t.Helper()

	body, err := imageledger.Marshal(entries)
	require.NoError(t, err)
	_, err = imageledger.Parse(body)
	require.NoError(t, err, "the fixture must carry a valid ledger")
	writeReleaseFilesTestFile(t, "dist/release-images.json", string(body))
}

// declareHandoff writes a producer's unsigned hand-off with one archive and,
// with buildSBOM, one SBOM in its bundle.
func declareHandoff(t *testing.T, buildSBOM bool) *apprelease.FileManifest {
	t.Helper()
	t.Chdir(t.TempDir())
	writeReleaseFilesTestFile(t, "dist/app.tar.gz", "archive")

	in := apprelease.DeclareReleaseFilesInput{SBOMBundleNaming: testSBOMBundleNaming, Assets: []string{"dist/app.tar.gz"}}

	if buildSBOM {
		writeReleaseFilesTestFile(t, "dist/app.cdx.json", `{"bomFormat":"CycloneDX"}`)

		in.SBOMs = []string{"dist/app.cdx.json"}
	}

	manifest, err := apprelease.DeclareReleaseFiles(in)
	require.NoError(t, err)

	return manifest
}

func bundleMembers(t *testing.T, path string) map[string]string {
	t.Helper()

	files, err := archive.ReadTarGzipFiles(path, 1<<20)
	require.NoError(t, err)

	members := map[string]string{}
	for _, file := range files {
		members[file.Name] = string(file.Body)
	}

	return members
}

func fromLedger() apprelease.WriteReleaseFileManifestInput {
	return apprelease.WriteReleaseFileManifestInput{
		Unsigned:         true,
		ReleaseImages:    apprelease.ImageSBOMs{Ledger: "dist/release-images.json", FromLedger: true, ReleaseTag: "v1.2.3"},
		SBOMBundleNaming: testSBOMBundleNaming,
	}
}

// A ledger a consumer's jobs handed over is bundled by its own pins: each
// pinned SBOM in dist, under the name the engine gives an index or a single
// platform's SBOM, and the inventories an index SBOM names. A base image
// is a build input whose SBOM is outside dist: it is not bundled and does not
// stop the release.
func TestHandoffLedgerImageSBOMsJoinTheSBOMBundle(t *testing.T) {
	for _, buildSBOM := range []bool{true, false} {
		t.Run(fmt.Sprintf("build SBOM %t", buildSBOM), func(t *testing.T) {
			before := declareHandoff(t, buildSBOM)

			indexed := writeIndexSBOM(t, "app")
			digest := "sha256:" + strings.Repeat("a", 64)

			single := imageledger.Entry{Role: "tool", Ref: "registry.example/org/tool@" + digest, Digest: digest, FinalTag: "registry.example/org/tool:v1.2.3", SBOM: "dist/tool-1.2.3-abcdef0-analyzed-container-linux-amd64-sbom.cyclonedx.json"}
			writeReleaseFilesTestFile(t, single.SBOM, `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library","name":"tool"}]}`)
			single.SBOMSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(readFile(t, single.SBOM))))

			base := imageledger.Entry{ImageKind: imageledger.ImageKindBase, Role: "base", Flavor: "core", BaseInputID: strings.Repeat("b", 64), Ref: "registry.example/org/base@" + digest, Digest: digest, SBOM: "premade-sboms/base-sbom-core.cyclonedx.json", SBOMSHA256: strings.Repeat("c", 64)}
			base.FinalTag = "registry.example/org/base:" + base.BaseInputID + "-core"

			writeImageLedger(t, indexed, single, base)
			// A file in dist that no ledger pin names is never bundled.
			writeReleaseFilesTestFile(t, "dist/consumer-1.2.3-abcdef0-analyzed-container-linux-amd64-sbom.cyclonedx.json", "{}")

			after, err := apprelease.WriteReleaseFileManifest(fromLedger())
			require.NoError(t, err)
			require.Equal(t, []apprelease.FileEntry{{Path: handoffBundle, Name: "app-1.2.3-sboms.tar.gz", Source: "sbom_bundle"}}, after.SBOMs)
			require.Empty(t, after.Evidence)

			want := map[string]string{}
			for _, name := range []string{"app-1.2.3-abcdef0-analyzed-container-index-sbom.cyclonedx.json", "app-1.2.3-abcdef0-analyzed-container-linux-amd64-sbom.cyclonedx.json", "app-1.2.3-abcdef0-analyzed-container-linux-arm64-sbom.cyclonedx.json", "tool-1.2.3-abcdef0-analyzed-container-linux-amd64-sbom.cyclonedx.json"} {
				want[name] = readFile(t, "dist/"+name)
			}

			if buildSBOM {
				want["app.cdx.json"] = `{"bomFormat":"CycloneDX"}`

				require.Equal(t, before.Assets, after.Assets)
			} else {
				require.Len(t, after.Assets, len(before.Assets)+1)
			}

			require.Equal(t, want, bundleMembers(t, handoffBundle))
			require.Contains(t, readFile(t, "dist/checksums.sha256"), fmt.Sprintf("%x  app-1.2.3-sboms.tar.gz\n", sha256.Sum256([]byte(readFile(t, handoffBundle)))))
			require.NoError(t, apprelease.ValidateReleaseChecksums(apprelease.ValidateReleaseChecksumsInput{}))
			require.NoError(t, apprelease.ValidateReleaseFileManifest(apprelease.ValidateReleaseFileManifestInput{Unsigned: true}))

			// The same SBOMs give the same bundle: a hand-off that declared
			// all of them at once has these bytes.
			completed := readFile(t, handoffBundle)

			t.Chdir(t.TempDir())

			for name, body := range want {
				writeReleaseFilesTestFile(t, "dist/"+name, body)
			}

			writeReleaseFilesTestFile(t, "dist/app.tar.gz", "archive")

			_, err = apprelease.DeclareReleaseFiles(apprelease.DeclareReleaseFilesInput{SBOMBundleNaming: testSBOMBundleNaming, Assets: []string{"dist/app.tar.gz"}, SBOMs: []string{"dist/*.json"}})
			require.NoError(t, err)
			require.Equal(t, completed, readFile(t, handoffBundle), "the completed bundle is reproducible from its SBOMs")
		})
	}
}

// The SBOM name ledger add --default-sbom records for a release image is
// one the SBOM bundle admits: a consumer that takes the default records a
// ledger the release accepts.
func TestHandoffLedgerAdmitsTheLedgerAddDefaultSBOM(t *testing.T) {
	declareHandoff(t, true)

	scratch := t.TempDir() + "/release-images.json"
	digest := "sha256:" + strings.Repeat("a", 64)
	require.NoError(t, appcontainer.AddLedgerEntry(t.Context(), nil, io.Discard, appcontainer.AddLedgerEntryInput{
		LedgerPath: scratch, ReleaseTag: "v1.2.3", Role: "tool", ImageKind: string(imageledger.ImageKindRelease),
		ImageName: "registry.example/org/tool", Digest: digest, FinalTagName: "v1.2.3",
		DefaultSBOM: true, SourceSHA: "abcdef0" + strings.Repeat("1", 33),
	}))

	body, err := os.ReadFile(scratch)
	require.NoError(t, err)
	entries, err := imageledger.Parse(body)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	entry := entries[0]
	writeReleaseFilesTestFile(t, entry.SBOM, `{"bomFormat":"CycloneDX","specVersion":"1.6"}`)
	entry.SBOMSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(readFile(t, entry.SBOM))))
	writeImageLedger(t, entry)

	_, err = apprelease.WriteReleaseFileManifest(fromLedger())
	require.NoError(t, err)
	require.Contains(t, bundleMembers(t, handoffBundle), strings.TrimPrefix(entry.SBOM, "dist/"))
}

// Every admitted file is held to the checksum the ledger or the index SBOM
// records, and a refused hand-off keeps its bundle, checksums and manifest.
func TestHandoffLedgerImageSBOMRefusalsLeaveTheHandoffUnchanged(t *testing.T) {
	for _, defect := range []string{"changed index", "missing index", "changed inventory", "missing inventory", "index of another image", "shared SBOM file", "named and from ledger", "without a ledger", "image SBOM as evidence", "bundle not the checksummed one", "no name for a new bundle", "pinned outside dist", "release image without a pin", "legacy name", "legacy single-platform name", "another name", "another version", "another commit", "index under a platform name"} {
		t.Run(defect, func(t *testing.T) {
			declareHandoff(t, defect != "no name for a new bundle")

			first, second := writeIndexSBOM(t, "app"), writeIndexSBOM(t, "worker")
			in := fromLedger()
			// rename moves the second image's index SBOM, pin and all.
			rename := func(name string) {
				writeReleaseFilesTestFile(t, "dist/"+name, readFile(t, second.SBOM))
				second.SBOM = "dist/" + name
			}

			switch defect {
			case "changed index":
				writeReleaseFilesTestFile(t, first.SBOM, readFile(t, first.SBOM)+"\n")
			case "missing index":
				require.NoError(t, os.Remove(first.SBOM))
			case "changed inventory":
				writeReleaseFilesTestFile(t, "dist/app-1.2.3-abcdef0-analyzed-container-linux-arm64-sbom.cyclonedx.json", `{"bomFormat":"CycloneDX","specVersion":"1.6"}`)
			case "missing inventory":
				require.NoError(t, os.Remove("dist/worker-1.2.3-abcdef0-analyzed-container-linux-amd64-sbom.cyclonedx.json"))
			case "index of another image":
				second.SBOMSHA256 = first.SBOMSHA256
				writeReleaseFilesTestFile(t, second.SBOM, readFile(t, first.SBOM))
			case "shared SBOM file":
				second.SBOM, second.SBOMSHA256 = first.SBOM, first.SBOMSHA256
			case "named and from ledger":
				in.ReleaseImages.Files = []string{first.SBOM}
			case "without a ledger":
				in.ReleaseImages.Ledger = ""
			case "image SBOM as evidence":
				in.EvidenceFiles = []string{"dist/app-1.2.3-abcdef0-analyzed-container-linux-amd64-sbom.cyclonedx.json"}
			case "bundle not the checksummed one":
				// A readable bundle, so only its checksums line refuses it.
				var other bytes.Buffer

				writer := archive.NewTarGzipWriter(&other)
				require.NoError(t, writer.AddBytes("swapped.cdx.json", []byte(`{"bomFormat":"CycloneDX"}`)))
				require.NoError(t, writer.Close())
				writeReleaseFilesTestFile(t, handoffBundle, other.String())
			case "no name for a new bundle":
				in.SBOMBundleNaming = apprelease.SBOMBundleNaming{}
			case "pinned outside dist":
				// Attested but never published: the bundle must hold it.
				second.SBOM = "evidence/worker-1.2.3-abcdef0-analyzed-container-index-sbom.cyclonedx.json"
				writeReleaseFilesTestFile(t, second.SBOM, readFile(t, "dist/worker-1.2.3-abcdef0-analyzed-container-index-sbom.cyclonedx.json"))
			case "release image without a pin":
				// Its SBOM would be made at signing, after the bundle is final.
				second.SBOMSHA256 = ""
			case "legacy name":
				rename("image-sbom-worker.cyclonedx.json")
			case "legacy single-platform name":
				second.SBOM = "dist/image-sbom-worker.cyclonedx.json"
				writeReleaseFilesTestFile(t, second.SBOM, `{"bomFormat":"CycloneDX","specVersion":"1.6"}`)
				second.SBOMSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(readFile(t, second.SBOM))))
			case "another name":
				rename("other-1.2.3-abcdef0-analyzed-container-index-sbom.cyclonedx.json")
			case "another version":
				rename("worker-1.2.4-abcdef0-analyzed-container-index-sbom.cyclonedx.json")
			case "another commit":
				rename("worker-1.2.3-1234567-analyzed-container-index-sbom.cyclonedx.json")
			case "index under a platform name":
				rename("worker-1.2.3-abcdef0-analyzed-container-linux-amd64-sbom.cyclonedx.json")
			}

			writeImageLedger(t, first, second)

			before := map[string]string{"dist/release-files.json": readFile(t, "dist/release-files.json"), "dist/checksums.sha256": readFile(t, "dist/checksums.sha256")}
			if defect != "no name for a new bundle" {
				before[handoffBundle] = readFile(t, handoffBundle)
			}

			_, err := apprelease.WriteReleaseFileManifest(in)
			require.Error(t, err)

			for path, body := range before {
				require.Equal(t, body, readFile(t, path), path)
			}

			switch defect {
			case "release image without a pin":
				require.ErrorContains(t, err, "no pinned SBOM")
			case "legacy name", "legacy single-platform name", "another name", "another version", "another commit":
				require.ErrorContains(t, err, "is not named worker-1.2.3-<sha>-analyzed-container-")
			case "index under a platform name":
				require.ErrorContains(t, err, "an index SBOM only under an index name")
			}

			if defect == "no name for a new bundle" {
				require.ErrorIs(t, err, errs.ErrUsage)
				require.NoFileExists(t, handoffBundle)
			}
		})
	}
}

// The SBOM bundle is made final before anything that covers it exists. The
// one step that changes it runs on an unsigned hand-off only: it refuses a
// dist that already holds a signature of the bundle or the provenance
// statement, it cannot run in the finalization that follows signing, and the
// checksums line it leaves is the line of the bundle in dist. A bundle that
// changed after its line was written does not pass the checksum validation
// the provenance and signing steps start with.
func TestSBOMBundleIsFinalBeforeAnythingCoversIt(t *testing.T) {
	for _, covered := range []string{"", handoffBundle + ".asc", handoffBundle + ".sig", handoffBundle + ".bundle", "dist/checksums.sha256.asc", "dist/slsa-provenance.intoto.json"} {
		t.Run("already written "+covered, func(t *testing.T) {
			declareHandoff(t, true)
			writeImageLedger(t, writeIndexSBOM(t, "app"))

			handedOver := readFile(t, handoffBundle)
			if covered != "" {
				writeReleaseFilesTestFile(t, covered, "written before the bundle was final")
			}

			_, err := apprelease.WriteReleaseFileManifest(fromLedger())
			if covered != "" {
				require.ErrorIs(t, err, errs.ErrValidation)
				require.Equal(t, handedOver, readFile(t, handoffBundle), "a covered bundle is not changed")

				return
			}

			require.NoError(t, err)

			completed := readFile(t, handoffBundle)
			require.NotEqual(t, handedOver, completed)

			line := fmt.Sprintf("%x  app-1.2.3-sboms.tar.gz\n", sha256.Sum256([]byte(completed)))
			require.Contains(t, readFile(t, "dist/checksums.sha256"), line)
			require.NotContains(t, readFile(t, "dist/checksums.sha256"), fmt.Sprintf("%x  app-1.2.3-sboms.tar.gz\n", sha256.Sum256([]byte(handedOver))), "no checksums line of the earlier bundle remains")
			require.NoError(t, apprelease.ValidateReleaseChecksums(apprelease.ValidateReleaseChecksumsInput{}))

			// Signing follows; the bundle cannot be completed again after it.
			writeReleaseFilesTestFile(t, handoffBundle+".sig", "signature")

			_, err = apprelease.WriteReleaseFileManifest(fromLedger())
			require.ErrorIs(t, err, errs.ErrValidation)

			finalize := fromLedger()
			finalize.Unsigned = false
			_, err = apprelease.WriteReleaseFileManifest(finalize)
			require.ErrorIs(t, err, errs.ErrUsage, "finalization after signing never changes the bundle")
			require.Equal(t, completed, readFile(t, handoffBundle))

			// A bundle whose bytes are not the ones its checksums line records
			// is refused where the statement and the signatures begin.
			writeReleaseFilesTestFile(t, handoffBundle, handedOver)
			require.Error(t, apprelease.ValidateReleaseChecksums(apprelease.ValidateReleaseChecksumsInput{}))
		})
	}
}
