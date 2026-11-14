// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/osfs"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

type migrationHandoffDownloader struct {
	files map[string]map[string]string
	calls []string
}

func (d *migrationHandoffDownloader) DownloadRunArtifact(_ context.Context, in provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
	d.calls = append(d.calls, in.Name)

	files, ok := d.files[in.Name]
	if !ok {
		return provider.RunArtifactInfo{}, errs.ErrMissingInput
	}

	for name, body := range files {
		if err := os.WriteFile(filepath.Join(in.Dir, name), []byte(body), 0o600); err != nil {
			return provider.RunArtifactInfo{}, err
		}
	}

	return provider.RunArtifactInfo{Name: in.Name, FileCount: len(files)}, nil
}

// Exercise the actual Go owners used by build-goreleaser, upload-dist and
// assemble-release-dist, not a second shell implementation or a live provider.
func TestMigrationHandoffPreservesProducerManifestAndBindsImageEvidence(t *testing.T) {
	t.Chdir(t.TempDir())

	writeReleaseFilesTestFile(t, "dist/app.tar.gz", "release archive\n")
	writeReleaseFilesTestFile(t, "dist/app.tar.gz.sbom.json", "{}\n")
	writeReleaseFilesTestFile(t, "dist/app_linux_amd64/app", "raw binary, needed before pruning\n")
	writeReleaseFilesTestFile(t, "dist/release-notes.md", "product release notes\n")
	writeReleaseFilesTestFile(t, "dist/app_checksums.txt",
		testFileSHA256(t, "dist/app.tar.gz")+"  app.tar.gz\n"+
			testFileSHA256(t, "dist/app.tar.gz.sbom.json")+"  app.tar.gz.sbom.json\n")
	writeReleaseFilesTestFile(t, "dist/artifacts.json", `[
  {"path":"dist/app.tar.gz","type":"Archive"},
  {"path":"dist/app.tar.gz.sbom.json","type":"SBOM"},
  {"path":"dist/app_checksums.txt","type":"Checksum"},
  {"path":"dist/app_linux_amd64/app","type":"Binary"}
]`)

	producer, err := apprelease.ImportGoReleaserFileManifest(apprelease.ImportGoReleaserFileManifestInput{})
	if err != nil {
		t.Fatal(err)
	}

	manifest := readFile(t, "dist/release-files.json")
	if _, err := apprelease.PrepareDist(t.Context(), osfs.New(), nil, io.Discard, apprelease.PrepareDistInput{Path: "dist", PruneDirs: true}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat("dist/app_linux_amd64"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw binaries survived explicit producer pruning: %v", err)
	}

	// Simulate the job boundary using only transferred public files, notes and
	// the producer manifest. Fan-in must not need GoReleaser metadata again.
	files := make(map[string]string, len(producer.Assets)+2)
	for _, asset := range producer.Assets {
		files[asset.Name] = readFile(t, asset.Path)
	}

	files["release-files.json"], files["release-notes.md"] = manifest, readFile(t, "dist/release-notes.md")
	if err := os.Rename("dist", "producer-dist"); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir("dist", 0o700); err != nil {
		t.Fatal(err)
	}

	download := &migrationHandoffDownloader{files: map[string]map[string]string{
		"release-files": files,
		"release-images": {
			"release-images.json":          "[]\n",
			"trivy-results-app-amd64.json": "{\"Results\":[]}\n",
		},
	}}
	sink := fakeoutputsink.New(t)

	assembled, err := apprelease.AssembleDist(t.Context(), download, osfs.New(), sink, io.Discard, apprelease.AssembleDistInput{
		ArtifactNames: "release-files\nrelease-images", Path: "dist/", PruneDirs: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(download.calls, []string{"release-files", "release-images"}) || sink.Single("digest") != assembled.Digest {
		t.Fatalf("handoff routes or digest channel changed: calls=%v digest=%s", download.calls, assembled.Digest)
	}

	if readFile(t, "dist/release-files.json") != manifest {
		t.Fatal("fan-in changed the producer manifest instead of preserving it")
	}

	if err := apprelease.ValidateReleaseChecksums(apprelease.ValidateReleaseChecksumsInput{}); err != nil {
		t.Fatalf("producer manifest failed after transfer without build metadata: %v", err)
	}

	if err := apprelease.VerifyDist("dist/", assembled.Digest); err != nil {
		t.Fatalf("signer-side verification refused the intact fan-in: %v", err)
	}

	for _, name := range []string{"app.tar.gz", "app.tar.gz.sbom.json", "release-files.json", "release-notes.md", "release-images.json", "trivy-results-app-amd64.json"} {
		t.Run("tampered/"+name, func(t *testing.T) {
			path := filepath.Join("dist", name)
			original := readFile(t, path)
			writeReleaseFilesTestFile(t, path, original+"tampered\n")
			t.Cleanup(func() { writeReleaseFilesTestFile(t, path, original) })

			if err := apprelease.VerifyDist("dist/", assembled.Digest); !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("modified %s crossed signer digest boundary: %v", name, err)
			}
		})
	}
}
