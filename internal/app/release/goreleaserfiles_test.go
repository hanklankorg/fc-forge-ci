// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestImportGoReleaserFileManifest_BeforeSigningThenFinalize(t *testing.T) {
	t.Chdir(t.TempDir())
	writeUnsignedReleaseFilesFixture(t)
	writeReleaseFilesTestFile(t, "dist/evidence.json", `{"version":1}`)

	manifest, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{
		SBOMBundleNaming: testSBOMBundleNaming,
		EvidenceFiles:    []string{"dist/evidence.json"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// GoReleaser's SBOM is published in the SBOM bundle, not beside it, and
	// the checksums file is the engine's.
	want := []string{testSBOMBundle, "app.apk", "app.deb", "app.rpm", "app.tar.gz", "checksums.sha256", "evidence.json"}

	names := make([]string, 0, len(manifest.Assets))
	for _, entry := range manifest.Assets {
		names = append(names, entry.Name)
	}

	slices.Sort(names)

	if !slices.Equal(names, want) || len(manifest.Provenance) != 0 {
		t.Fatalf("unsigned assets = %v, provenance = %v", names, manifest.Provenance)
	}

	evidence, err := apprelease.ListReleaseFiles(apprelease.ListReleaseFilesInput{Section: "evidence"})
	if err != nil || !slices.Equal(evidence, []string{"dist/evidence.json"}) {
		t.Fatalf("evidence = %v, err = %v", evidence, err)
	}

	sboms, err := apprelease.ListReleaseFiles(apprelease.ListReleaseFilesInput{Section: "sboms"})
	if err != nil || !slices.Equal(sboms, []string{"dist/" + testSBOMBundle}) {
		t.Fatalf("sboms = %v, err = %v", sboms, err)
	}

	// checksums.sha256 lists the archive, the packages and the SBOM bundle,
	// by name, as `release files declare` writes it.
	var wantChecksums strings.Builder
	for _, name := range []string{testSBOMBundle, "app.apk", "app.deb", "app.rpm", "app.tar.gz"} {
		wantChecksums.WriteString(testFileSHA256(t, "dist/"+name) + "  " + name + "\n")
	}

	if got := readFile(t, "dist/checksums.sha256"); got != wantChecksums.String() {
		t.Fatalf("checksums = %q, want %q", got, wantChecksums.String())
	}

	if validateErr := apprelease.ValidateReleaseChecksums(apprelease.ValidateReleaseChecksumsInput{}); validateErr != nil {
		t.Fatal(validateErr)
	}

	unsigned := readFile(t, "dist/release-files.json")
	if _, finalizeErr := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{}); !errors.Is(finalizeErr, errs.ErrMissingInput) {
		t.Fatalf("finalization without provenance = %v, want missing input", finalizeErr)
	}

	if readFile(t, "dist/release-files.json") != unsigned {
		t.Fatal("failed finalization changed the unsigned manifest")
	}

	writeReleaseFilesTestFile(t, "dist/slsa-provenance.intoto.json", "provenance\n")
	writeReleaseFilesTestFile(t, "dist/slsa-provenance.intoto.json.bundle", "provenance signature\n")
	writeReleaseFilesTestFile(t, "dist/app.deb.sig", "package signature\n")
	writeReleaseFilesTestFile(t, "dist/checksums.sha256.bundle", "checksums signature\n")

	// Evidence is no checksum subject: unsigned, it is not published.
	if _, finalizeErr := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{}); !errors.Is(finalizeErr, errs.ErrValidation) ||
		!strings.Contains(finalizeErr.Error(), "dist/evidence.json.bundle") {
		t.Fatalf("finalization with unsigned evidence = %v, want the missing bundle refused", finalizeErr)
	}

	if readFile(t, "dist/release-files.json") != unsigned {
		t.Fatal("failed finalization changed the unsigned manifest")
	}

	writeReleaseFilesTestFile(t, "dist/evidence.json.bundle", "evidence signature\n")

	final, err := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{})
	if err != nil {
		t.Fatal(err)
	}

	assertReleaseAssetsInclude(t, final.Assets, "app.deb.sig", "checksums.sha256.bundle", "evidence.json", "evidence.json.bundle", "slsa-provenance.intoto.json", "slsa-provenance.intoto.json.bundle")

	if !slices.Equal(final.Evidence, manifest.Evidence) || len(final.Provenance) != 1 {
		t.Fatalf("final classifications = %+v", final)
	}

	if validateErr := apprelease.ValidateReleaseChecksums(apprelease.ValidateReleaseChecksumsInput{}); validateErr != nil {
		t.Fatal(validateErr)
	}
}

func TestImportGoReleaserFileManifest_RefusesInvalidEvidence(t *testing.T) {
	for _, test := range []struct {
		name     string
		evidence []string
		wantErr  error
	}{
		{name: "missing", evidence: []string{"dist/missing.json"}, wantErr: errs.ErrMissingInput},
		{name: "symlink", evidence: []string{"dist/link.json"}, wantErr: errs.ErrValidation},
		{name: "outside dist", evidence: []string{"outside.json"}, wantErr: errs.ErrValidation},
		{name: "reclassifies archive", evidence: []string{"dist/app.tar.gz"}, wantErr: errs.ErrValidation},
		{name: "duplicate", evidence: []string{"dist/plan.json", "dist/plan.json"}, wantErr: errs.ErrValidation},
		{name: "internal ledger", evidence: []string{"dist/release-images.json"}, wantErr: errs.ErrValidation},
		{name: "release plan", evidence: []string{"dist/release-plan.json"}, wantErr: errs.ErrValidation},
		{name: "snapshot plan", evidence: []string{"dist/snapshot-release-plan.json"}, wantErr: errs.ErrValidation},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeUnsignedReleaseFilesFixture(t)
			writeReleaseFilesTestFile(t, "outside.json", "outside\n")
			writeReleaseFilesTestFile(t, "dist/plan.json", "evidence\n")
			writeReleaseFilesTestFile(t, "dist/release-images.json", "[]\n")
			writeReleaseFilesTestFile(t, "dist/release-plan.json", `{"version":1}`)
			writeReleaseFilesTestFile(t, "dist/snapshot-release-plan.json", `{"version":1}`)

			if err := os.Symlink("../outside.json", "dist/link.json"); err != nil {
				t.Fatal(err)
			}

			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{SBOMBundleNaming: testSBOMBundleNaming, EvidenceFiles: test.evidence})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("err = %v, want %v", err, test.wantErr)
			}

			if _, err := os.Lstat("dist/release-files.json"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed initialization wrote a manifest: %v", err)
			}
		})
	}
}

func TestImportGoReleaserFileManifest_PreservesExistingManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	writeUnsignedReleaseFilesFixture(t)
	writeReleaseFilesTestFile(t, "dist/release-files.json", "existing manifest\n")

	_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{SBOMBundleNaming: testSBOMBundleNaming})
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("err = %v, want existing manifest refusal", err)
	}

	if readFile(t, "dist/release-files.json") != "existing manifest\n" {
		t.Fatal("initialization overwrote the existing manifest")
	}
}

// A producer's signature or provenance statement in the dist would be
// published as the signer's by finalization, so the import refuses each one,
// whichever imported file it sits next to, and writes no manifest.
func TestImportGoReleaserFileManifest_RefusesAnAlreadySignedDist(t *testing.T) {
	for _, planted := range []string{
		"dist/app.tar.gz.sig",
		"dist/app.tar.gz.bundle",
		"dist/app.deb.sig",
		"dist/checksums.sha256.bundle",
		"dist/evidence.json.bundle",
		"dist/slsa-provenance.intoto.json",
		"dist/slsa-provenance.intoto.json.bundle",
	} {
		t.Run(planted, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeUnsignedReleaseFilesFixture(t)
			writeReleaseFilesTestFile(t, "dist/evidence.json", `{"version":1}`)
			writeReleaseFilesTestFile(t, planted, "producer supplied\n")

			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{
				SBOMBundleNaming: testSBOMBundleNaming,
				EvidenceFiles:    []string{"dist/evidence.json"},
			})
			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("err = %v, want the signed dist refused", err)
			}

			if _, err := os.Lstat("dist/release-files.json"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused import wrote a manifest: %v", err)
			}
		})
	}
}

// A release has one checksums file, the engine's checksums.sha256. The
// import refuses the checksums GoReleaser writes, whatever their name, and
// leaves no hand-off file behind.
func TestImportGoReleaserFileManifest_RefusesGoReleaserChecksums(t *testing.T) {
	for name, checksums := range map[string]string{
		"one file":         "dist/app_1.2.3_checksums.txt",
		"split":            "dist/app.tar.gz.sha256",
		"engine file name": "dist/checksums.sha256",
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeUnsignedReleaseFilesFixture(t)
			writeReleaseFilesTestFile(t, checksums, testFileSHA256(t, "dist/app.tar.gz")+"  app.tar.gz\n")

			artifacts := strings.Replace(readFile(t, "dist/artifacts.json"), "[\n", "[\n  {\"path\":\""+checksums+"\",\"type\":\"Checksum\"},\n", 1)
			writeReleaseFilesTestFile(t, "dist/artifacts.json", artifacts)

			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{SBOMBundleNaming: testSBOMBundleNaming})
			if !errors.Is(err, errs.ErrInvalidConfig) || !strings.Contains(err.Error(), checksums) || !strings.Contains(err.Error(), "checksum.disable: true") {
				t.Fatalf("err = %v, want GoReleaser's checksums refused", err)
			}

			for _, path := range []string{"dist/release-files.json", "dist/" + testSBOMBundle} {
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("refused import left %s: %v", path, statErr)
				}
			}
		})
	}
}

// writeUnsignedReleaseFilesFixture is the fixture as GoReleaser hands it over
// with checksum.disable: no checksums file, no provenance statement and no
// signature next to a file the import declares.
func writeUnsignedReleaseFilesFixture(t *testing.T) {
	t.Helper()
	writeReleaseFilesFixture(t)

	for _, path := range []string{
		"dist/slsa-provenance.intoto.json", "dist/slsa-provenance.intoto.json.bundle",
		"dist/app.deb.sig", "dist/app.rpm.sig", "dist/app.apk.sig",
		"dist/app.tar.gz.sbom.json.bundle",
		"dist/app_checksums.txt", "dist/app_checksums.txt.bundle",
	} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	writeReleaseFilesTestFile(t, "dist/artifacts.json", `[
  {"path":"dist/app.tar.gz","type":"Archive"},
  {"path":"dist/app.deb","type":"LinuxPackage"},
  {"path":"dist/app.rpm","type":"LinuxPackage"},
  {"path":"dist/app.apk","type":"LinuxPackage"},
  {"path":"dist/app.tar.gz.sbom.json","type":"SBOM"},
  {"path":"dist/metadata.json","type":"Metadata"},
  {"path":"dist/internal-binary","type":"Binary"}
]`)
}
