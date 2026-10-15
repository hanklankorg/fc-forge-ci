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
	writeReleaseFilesTestFile(t, "dist/release-plan.json", `{"version":1}`)

	manifest, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{
		EvidenceFiles: []string{"dist/release-plan.json"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"app.apk", "app.deb", "app.rpm", "app.tar.gz", "app.tar.gz.sbom.json", "app_checksums.txt", "release-plan.json"}

	names := make([]string, 0, len(manifest.Assets))
	for _, entry := range manifest.Assets {
		names = append(names, entry.Name)
	}

	slices.Sort(names)

	if !slices.Equal(names, want) || len(manifest.Provenance) != 0 {
		t.Fatalf("unsigned assets = %v, provenance = %v", names, manifest.Provenance)
	}

	evidence, err := apprelease.ListReleaseFiles(apprelease.ListReleaseFilesInput{Section: "evidence"})
	if err != nil || !slices.Equal(evidence, []string{"dist/release-plan.json"}) {
		t.Fatalf("evidence = %v, err = %v", evidence, err)
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
	writeReleaseFilesTestFile(t, "dist/app_checksums.txt.bundle", "checksums signature\n")

	// Evidence is no checksum subject: unsigned, it is not published.
	if _, finalizeErr := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{}); !errors.Is(finalizeErr, errs.ErrValidation) ||
		!strings.Contains(finalizeErr.Error(), "dist/release-plan.json.bundle") {
		t.Fatalf("finalization with unsigned evidence = %v, want the missing bundle refused", finalizeErr)
	}

	if readFile(t, "dist/release-files.json") != unsigned {
		t.Fatal("failed finalization changed the unsigned manifest")
	}

	writeReleaseFilesTestFile(t, "dist/release-plan.json.bundle", "evidence signature\n")

	final, err := apprelease.WriteReleaseFileManifest(apprelease.WriteReleaseFileManifestInput{})
	if err != nil {
		t.Fatal(err)
	}

	assertReleaseAssetsInclude(t, final.Assets, "app.deb.sig", "app_checksums.txt.bundle", "release-plan.json", "release-plan.json.bundle", "slsa-provenance.intoto.json", "slsa-provenance.intoto.json.bundle")

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
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeUnsignedReleaseFilesFixture(t)
			writeReleaseFilesTestFile(t, "outside.json", "outside\n")
			writeReleaseFilesTestFile(t, "dist/plan.json", "evidence\n")
			writeReleaseFilesTestFile(t, "dist/release-images.json", "[]\n")

			if err := os.Symlink("../outside.json", "dist/link.json"); err != nil {
				t.Fatal(err)
			}

			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{EvidenceFiles: test.evidence})
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

	_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{})
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
		"dist/app_checksums.txt.bundle",
		"dist/release-plan.json.bundle",
		"dist/slsa-provenance.intoto.json",
		"dist/slsa-provenance.intoto.json.bundle",
	} {
		t.Run(planted, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeUnsignedReleaseFilesFixture(t)
			writeReleaseFilesTestFile(t, "dist/release-plan.json", `{"version":1}`)
			writeReleaseFilesTestFile(t, planted, "producer supplied\n")

			_, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{
				EvidenceFiles: []string{"dist/release-plan.json"},
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

// writeUnsignedReleaseFilesFixture is the fixture as a producer hands it over:
// no provenance statement and no signature next to a file the import declares.
func writeUnsignedReleaseFilesFixture(t *testing.T) {
	t.Helper()
	writeReleaseFilesFixture(t)

	for _, path := range []string{
		"dist/slsa-provenance.intoto.json", "dist/slsa-provenance.intoto.json.bundle",
		"dist/app.deb.sig", "dist/app.rpm.sig", "dist/app.apk.sig",
		"dist/app.tar.gz.sbom.json.bundle", "dist/app_checksums.txt.bundle",
	} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}
