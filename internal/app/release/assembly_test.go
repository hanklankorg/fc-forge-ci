// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeprovider"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// TestAssemble_StagesEveryProducerLayoutIntoOneManifest feeds Assemble the
// shapes different producers leave behind — a Maven target directory, loose Go
// binaries, Android/iOS/Windows packages, an extracted binaries tree, SBOMs
// beside their service and in a scan output directory, an attached file matched
// by glob -- and requires their exact bytes and provenance in one staged layout.
//
// Both the return value and the written handoff must match an independent
// expectation: agreement with each other alone can preserve the same mistake.
func TestAssemble_StagesEveryProducerLayoutIntoOneManifest(t *testing.T) {
	fsys := testfs.NewReal(t)
	t.Chdir(fsys.Root)

	fsys.WriteFile(filepath.Join("release-artifacts", "target", "app.jar"), []byte("jar"))
	fsys.WriteFile(filepath.Join("release-artifacts", "target", "original-app.jar"), []byte("original"))
	fsys.WriteFile(filepath.Join("release-artifacts", "service-linux-amd64"), []byte("go binary"))
	fsys.WriteFile(filepath.Join("release-artifacts", "app.apk"), []byte("apk"))
	fsys.WriteFile(filepath.Join("release-artifacts", "app.aab"), []byte("android bundle\x00payload"))
	fsys.WriteFile(filepath.Join("release-artifacts", "app.exe"), []byte("windows executable\x00payload"))
	fsys.WriteFile(filepath.Join("release-artifacts", "app.ipa"), []byte("ipa"))
	fsys.WriteFile(filepath.Join("release-artifacts", "binaries", "darwin", "service-darwin-arm64"), []byte("extracted"))
	fsys.WriteFile(filepath.Join("release-artifacts", "release-images.json"), []byte(`{}`))
	fsys.WriteFile(filepath.Join("release-artifacts", "release-images-ledger.json"), []byte(`{}`))
	fsys.WriteFile(filepath.Join("extra", "nested", "readme.txt"), []byte("attached"))
	fsys.WriteFile(filepath.Join("services", "api", "api-sbom.spdx.json"), []byte(`{"spdxVersion":"2.3"}`))
	fsys.WriteFile(filepath.Join("sbom-artifacts", "web-analyzed-container-linux-amd64-sbom.spdx.json"), []byte(`{"name":"web-container","spdxVersion":"SPDX-2.3"}`))

	asm, err := apprelease.Assemble(&bytes.Buffer{}, apprelease.AssembleInput{
		ConfigPlanJSON: releaseAssemblyConfigJSON(t),
		ArtifactTransferPlanJSON: mustReleaseJSON(t, pipeline.ArtifactTransferPlan{
			Version: pipeline.ArtifactTransferPlanVersion,
			Items: []pipeline.ArtifactTransfer{
				{Kind: pipeline.ArtifactTransferBuildArtifact, Name: "compiled-release-42", Path: "./release-artifacts", Required: true},
				{Kind: pipeline.ArtifactTransferExtractedBinaries, Name: "extracted-release-43", Path: "./release-artifacts/binaries", Required: false},
				{Kind: pipeline.ArtifactTransferAnalyzedContainerSBOM, NameTemplate: "analyzed-web-{run_id}", Path: "./sbom-artifacts", Required: false},
			},
		}),
		AttachArtifacts: "extra/**/*.txt",
		ProjectName:     "my-app",
		Version:         "v1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Attachments and SBOMs deliberately have no transfer-artifact identity.
	// Once present, extracted binaries are required release assets even though
	// their CI transfer is optional; SBOM inputs remain optional.
	want := domainrelease.Assembly{
		Version: 1,
		Assets: []domainrelease.AssemblyFile{
			{
				Path: "release-files/assets/app.aab", Name: "app.aab",
				SourcePath: "release-artifacts/app.aab", SourceKind: "build_artifact",
				SourceArtifact: "compiled-release-42", Required: true,
			},
			{
				Path: "release-files/assets/app.apk", Name: "app.apk",
				SourcePath: "release-artifacts/app.apk", SourceKind: "build_artifact",
				SourceArtifact: "compiled-release-42", Required: true,
			},
			{
				Path: "release-files/assets/app.exe", Name: "app.exe",
				SourcePath: "release-artifacts/app.exe", SourceKind: "build_artifact",
				SourceArtifact: "compiled-release-42", Required: true,
			},
			{
				Path: "release-files/assets/app.ipa", Name: "app.ipa",
				SourcePath: "release-artifacts/app.ipa", SourceKind: "build_artifact",
				SourceArtifact: "compiled-release-42", Required: true,
			},
			{
				Path: "release-files/assets/app.jar", Name: "app.jar",
				SourcePath: "release-artifacts/target/app.jar", SourceKind: "build_artifact",
				SourceArtifact: "compiled-release-42", Required: true,
			},
			{
				Path: "release-files/assets/readme.txt", Name: "readme.txt",
				SourcePath: filepath.ToSlash(filepath.Join(fsys.Root, "extra", "nested", "readme.txt")), SourceKind: "attachment",
				SourceArtifact: "", Required: true,
			},
			{
				Path: "release-files/assets/service-darwin-arm64", Name: "service-darwin-arm64",
				SourcePath: "release-artifacts/binaries/darwin/service-darwin-arm64", SourceKind: "extracted_binaries",
				SourceArtifact: "extracted-release-43", Required: true,
			},
			{
				Path: "release-files/assets/service-linux-amd64", Name: "service-linux-amd64",
				SourcePath: "release-artifacts/service-linux-amd64", SourceKind: "build_artifact",
				SourceArtifact: "compiled-release-42", Required: true,
			},
		},
		SBOMs: []domainrelease.AssemblyFile{
			{
				Path: "release-files/sboms/api-sbom.spdx.json", Name: "api-sbom.spdx.json",
				SourcePath: "services/api/api-sbom.spdx.json", SourceKind: "generated_sbom",
				SourceArtifact: "", Required: false,
			},
			{
				Path: "release-files/sboms/web-analyzed-container-linux-amd64-sbom.spdx.json", Name: "web-analyzed-container-linux-amd64-sbom.spdx.json",
				SourcePath: "sbom-artifacts/web-analyzed-container-linux-amd64-sbom.spdx.json", SourceKind: "analyzed_container_sbom",
				SourceArtifact: "", Required: false,
			},
		},
		ChecksumFile:   "release-files/checksums.sha256",
		SBOMBundleFile: "release-files/assets/my-app-1.2.3-sboms.tar.gz",
		ProvenanceFile: "release-files/slsa-provenance.intoto.json",
	}

	for path, wantBody := range map[string]string{
		"release-files/assets/app.aab":                                          "android bundle\x00payload",
		"release-files/assets/app.apk":                                          "apk",
		"release-files/assets/app.exe":                                          "windows executable\x00payload",
		"release-files/assets/app.ipa":                                          "ipa",
		"release-files/assets/app.jar":                                          "jar",
		"release-files/assets/readme.txt":                                       "attached",
		"release-files/assets/service-darwin-arm64":                             "extracted",
		"release-files/assets/service-linux-amd64":                              "go binary",
		"release-files/sboms/api-sbom.spdx.json":                                `{"spdxVersion":"2.3"}`,
		"release-files/sboms/web-analyzed-container-linux-amd64-sbom.spdx.json": `{"name":"web-container","spdxVersion":"SPDX-2.3"}`,
	} {
		if got := readFile(t, path); got != wantBody {
			t.Errorf("staged file %s = %q, want %q", path, got, wantBody)
		}
	}

	var onDisk domainrelease.Assembly
	if err := json.Unmarshal([]byte(readFile(t, ".forge-ci/release-assembly.json")), &onDisk); err != nil {
		t.Fatalf("assembly manifest is missing or not valid JSON: %v", err)
	}

	for name, got := range map[string]*domainrelease.Assembly{"returned": asm, "on disk": &onDisk} {
		if !reflect.DeepEqual(got, &want) {
			t.Errorf("%s manifest = %+v\nwant %+v", name, got, want)
		}
	}
}

func TestAssemble_RejectsDuplicateAssetBasenames(t *testing.T) {
	fsys := testfs.NewReal(t)
	t.Chdir(fsys.Root)

	fsys.WriteFile(filepath.Join("release-artifacts", "one", "app.jar"), []byte("one"))
	fsys.WriteFile(filepath.Join("release-artifacts", "two", "app.jar"), []byte("two"))

	_, err := apprelease.Assemble(&bytes.Buffer{}, apprelease.AssembleInput{
		ConfigPlanJSON:           releaseAssemblyConfigJSON(t),
		ArtifactTransferPlanJSON: releaseAssemblyTransferJSON(t),
		ProjectName:              "my-app",
		Version:                  "v1.2.3",
	})
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "basename") {
		t.Fatalf("err = %v, want ErrValidation naming the duplicated basename", err)
	}
}

// TestAssemble_RejectsUnsafeAttachmentPattern covers both containment
// guards. --attach-artifacts is caller-supplied and is globbed against the
// workspace, so a pattern that is absolute and one that climbs out are
// refused separately -- and each row names its own guard, because the
// previous single assertion accepted either message for either input. A
// pattern that matches the pipeline's own release plan or image ledger is
// refused too: neither is a release file.
func TestAssemble_RejectsUnsafeAttachmentPattern(t *testing.T) {
	for name, testCase := range map[string]struct{ pattern, wantText string }{
		"climbs out of the workspace": {pattern: "../dist/*.zip", wantText: `contains ".."`},
		"absolute path":               {pattern: "/etc/*.zip", wantText: "must be relative to the workspace"},
		"the release plan by a glob":  {pattern: "dist/*.json", wantText: "release plan must not be a release file: release-plan.json: it is the pipeline's own"},
		"the image ledger":            {pattern: "release-images.json", wantText: "release image ledger must not be a release file: release-images.json: it is the pipeline's own"},
	} {
		t.Run(name, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			t.Chdir(fsys.Root)
			fsys.WriteFile(filepath.Join("dist", "release-plan.json"), []byte(`{}`))
			fsys.WriteFile("release-images.json", []byte(`[]`))

			_, err := apprelease.Assemble(&bytes.Buffer{}, apprelease.AssembleInput{
				ConfigPlanJSON:           releaseAssemblyConfigJSON(t),
				ArtifactTransferPlanJSON: releaseAssemblyTransferJSON(t),
				AttachArtifacts:          testCase.pattern,
				ProjectName:              "my-app",
				Version:                  "v1.2.3",
			})
			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("err = %v, want ErrValidation", err)
			}

			if !strings.Contains(err.Error(), testCase.wantText) {
				t.Errorf("err = %v, want it to name the guard that fired (%q)", err, testCase.wantText)
			}
		})
	}
}

// TestAssembly_EveryStageTakesItsFilesFromTheManifest runs the four consumers
// of an assembly manifest in the order a release runs them: bundle the SBOMs,
// checksum what ships, sign it, publish it. The claim they share is that none
// of them discovers files for itself — each file set comes from the manifest.
//
// Every stage writes what the next one reads, so they share a fixture and run
// in order. They are subtests because they are four separate claims: as one
// linear run of t.Fatal calls the first failure hid the rest, and the name had
// become a list of the stages rather than the behaviour they share.
func TestAssembly_EveryStageTakesItsFilesFromTheManifest(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()

	fsys.WriteFile(filepath.Join("release-files", "assets", "app.jar"), []byte("jar"))
	fsys.WriteFile(filepath.Join("release-files", "sboms", "api-sbom.spdx.json"), []byte(`{"spdxVersion":"2.3"}`))

	writeAssembly(t, domainrelease.DefaultAssemblyFile, domainrelease.Assembly{
		Version: domainrelease.AssemblyVersion,
		Assets: []domainrelease.AssemblyFile{
			{Path: "release-files/assets/app.jar", Name: "app.jar", Required: true},
		},
		SBOMs: []domainrelease.AssemblyFile{
			{Path: "release-files/sboms/api-sbom.spdx.json", Name: "api-sbom.spdx.json", Required: true},
		},
		ChecksumFile:   assemblyChecksumPath,
		SBOMBundleFile: assemblySBOMBundlePath,
		ProvenanceFile: assemblyProvenancePath,
	})

	t.Run("bundles the SBOMs the manifest lists, under the name it chose", func(t *testing.T) {
		bundleRes, err := apprelease.CreateSBOMBundle(context.Background(), nil, apprelease.SBOMBundleInput{
			ProjectName:  "my-app",
			Version:      "v1.2.3",
			AssemblyFile: domainrelease.DefaultAssemblyFile,
		}, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}

		if bundleRes.BundleName != assemblySBOMBundlePath || bundleRes.EntryCount != 1 {
			t.Errorf("SBOM bundle result = %+v", bundleRes)
		}

		assertBundleEntries(t, assemblySBOMBundlePath, []string{"api-sbom.spdx.json"})
	})

	t.Run("checksums what ships, naming each subject as it is published", func(t *testing.T) {
		count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{AssemblyFile: domainrelease.DefaultAssemblyFile})
		if err != nil {
			t.Fatal(err)
		}

		if count != 2 {
			t.Errorf("checksum count = %d, want 2", count)
		}

		// The written file is what a consumer runs sha256sum --check
		// against, so the subjects it names are the contract. A count
		// alone cannot tell the SBOM bundle from the SBOM it was made from.
		want := []string{"app.jar", "my-app-1.2.3-sboms.tar.gz"}

		entries := checksumEntries(t, assemblyChecksumPath)
		if got := checksumSubjects(t, assemblyChecksumPath); !slices.Equal(got, want) {
			t.Errorf("checksum subjects = %v, want %v", got, want)
		}

		// And the digests are real. The parsed digest column was never read,
		// so a manifest naming the right files with the wrong hashes -- what
		// a `sha256sum --check` consumer rejects -- satisfied everything here.
		wantJar := sha256.Sum256([]byte("jar"))
		if entries[0].Digest != hex.EncodeToString(wantJar[:]) {
			t.Errorf("app.jar digest = %q, want the sha256 of its contents %q",
				entries[0].Digest, hex.EncodeToString(wantJar[:]))
		}
	})

	t.Run("names the checksums and the place of the provenance statement", func(t *testing.T) {
		checksums, statement, err := apprelease.AssemblyProvenance(domainrelease.DefaultAssemblyFile)
		if err != nil {
			t.Fatal(err)
		}

		if statement != assemblyProvenancePath {
			t.Errorf("statement path = %q, want %q", statement, assemblyProvenancePath)
		}

		// The subjects are the files the checksums list, as on the forges
		// that take them from the release file manifest.
		body, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{
			Checksums:     bytes.NewReader(checksums),
			RepositoryURL: "https://forge.example/owner/repo", Ref: "v1.2.3",
			SHA:       "0123456789abcdef0123456789abcdef01234567",
			BuilderID: "https://forge.example/builder", InvocationID: "https://forge.example/run/1",
			StartedOn: "2026-01-01T00:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}

		for _, subject := range []string{`"name":"app.jar"`, `"name":"my-app-1.2.3-sboms.tar.gz"`} {
			if !strings.Contains(strings.ReplaceAll(string(body), " ", ""), subject) {
				t.Errorf("statement lacks subject %s:\n%s", subject, body)
			}
		}

		fsys.WriteFile(assemblyProvenancePath, body)
	})

	t.Run("signs the assets, the SBOM bundle, the checksum file and the statement", func(t *testing.T) {
		signer := &fakeSigner{}
		if err := apprelease.SignArtifacts(context.Background(), signer, &bytes.Buffer{}, apprelease.SignInput{AssemblyFile: domainrelease.DefaultAssemblyFile}); err != nil {
			t.Fatal(err)
		}

		wantSigned := []string{
			"release-files/assets/app.jar",
			assemblySBOMBundlePath,
			assemblyChecksumPath,
			assemblyProvenancePath,
		}
		if !slices.Equal(signer.signed, wantSigned) {
			t.Errorf("signed = %v, want %v", signer.signed, wantSigned)
		}
	})

	t.Run("publishes every signed file alongside its signature", func(t *testing.T) {
		prov := fakeprovider.New(t)
		if err := apprelease.CreateRelease(context.Background(), prov, &fakeFS{Files: map[string]bool{}}, &bytes.Buffer{}, apprelease.CreateReleaseInput{
			Tag:          "v1.2.3",
			Repository:   "owner/repo",
			AssemblyFile: domainrelease.DefaultAssemblyFile,
		}); err != nil {
			t.Fatal(err)
		}

		wantAssets := []string{
			"release-files/assets/app.jar",
			"release-files/assets/app.jar.asc",
			assemblySBOMBundlePath,
			assemblySBOMBundlePath + ".asc",
			assemblyChecksumPath,
			assemblyChecksumPath + ".asc",
			assemblyProvenancePath,
			assemblyProvenancePath + ".asc",
		}
		if assets := prov.CreateReleaseCalls()[0].Spec.Assets; !slices.Equal(assets, wantAssets) {
			t.Errorf("release assets = %v, want %v", assets, wantAssets)
		}
	})
}

func TestChecksums_AssemblyRequiresSBOMBundleWhenSBOMInputsExist(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()

	fsys.WriteFile(filepath.Join("release-files", "assets", "app.jar"), []byte("jar"))
	fsys.WriteFile(filepath.Join("release-files", "sboms", "api-sbom.spdx.json"), []byte(`{}`))

	writeAssembly(t, domainrelease.DefaultAssemblyFile, domainrelease.Assembly{
		Version: domainrelease.AssemblyVersion,
		Assets: []domainrelease.AssemblyFile{
			{Path: "release-files/assets/app.jar", Name: "app.jar", Required: true},
		},
		SBOMs: []domainrelease.AssemblyFile{
			{Path: "release-files/sboms/api-sbom.spdx.json", Name: "api-sbom.spdx.json", Required: true},
		},
		ChecksumFile:   "release-files/checksums.sha256",
		SBOMBundleFile: "release-files/assets/my-app-1.2.3-sboms.tar.gz",
	})

	_, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{AssemblyFile: domainrelease.DefaultAssemblyFile})
	if !errors.Is(err, errs.ErrMissingInput) || !strings.Contains(err.Error(), "run release sbom-bundle --assembly first") {
		t.Fatalf("err = %v, want ErrMissingInput telling the operator which step to run", err)
	}
}

func releaseAssemblyConfigJSON(t *testing.T) string {
	t.Helper()

	return mustReleaseJSON(t, assemblyConfigPlan(t, config.Artifact{
		Name: "api", ProjectType: projecttype.Go, WorkingDirectory: "services/api",
	}))
}

func releaseAssemblyTransferJSON(t *testing.T) string {
	t.Helper()

	return mustReleaseJSON(t, pipeline.ArtifactTransferPlan{
		Version: pipeline.ArtifactTransferPlanVersion,
		Items: []pipeline.ArtifactTransfer{
			{Kind: pipeline.ArtifactTransferBuildArtifact, Name: "build-artifacts", Path: "./release-artifacts", Required: true},
			{Kind: pipeline.ArtifactTransferExtractedBinaries, Name: "extracted-binaries", Path: "./release-artifacts/binaries", Required: false},
			{Kind: pipeline.ArtifactTransferAnalyzedContainerSBOM, NameTemplate: "analyzed-container-sbom-{run_id}", Path: "./sbom-artifacts", Required: false},
		},
	})
}

// The two files an assembly manifest names for the release to generate. Every
// stage after Assemble takes them from the manifest rather than deriving them,
// so the test states each path once and asserts against that.
const (
	assemblyChecksumPath   = "release-files/checksums.sha256"
	assemblySBOMBundlePath = "release-files/assets/my-app-1.2.3-sboms.tar.gz"
	assemblyProvenancePath = "release-files/slsa-provenance.intoto.json"
)

// checksumEntry is one line of a sha256sum manifest.
type checksumEntry struct {
	Digest  string
	Subject string
}

// checksumEntries parses a sha256sum manifest in the order it lists its lines.
// sha256sum separates digest from subject with two spaces.
func checksumEntries(t *testing.T, path string) []checksumEntry {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(readFile(t, path)), "\n")

	entries := make([]checksumEntry, 0, len(lines))

	for _, line := range lines {
		digest, subject, found := strings.Cut(line, "  ")
		if !found {
			t.Fatalf("%s: malformed checksum line %q", path, line)
		}

		entries = append(entries, checksumEntry{Digest: digest, Subject: subject})
	}

	return entries
}

// checksumSubjects returns just the file names a manifest lists, in order.
func checksumSubjects(t *testing.T, path string) []string {
	t.Helper()

	entries := checksumEntries(t, path)

	subjects := make([]string, 0, len(entries))
	for _, entry := range entries {
		subjects = append(subjects, entry.Subject)
	}

	return subjects
}

func writeAssembly(t *testing.T, path string, asm domainrelease.Assembly) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir assembly dir: %v", err)
	}

	if err := os.WriteFile(path, []byte(mustReleaseJSON(t, asm)), 0o600); err != nil {
		t.Fatalf("write assembly: %v", err)
	}
}

func mustReleaseJSON(t *testing.T, value any) string {
	t.Helper()

	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}

	return string(body)
}

// bundleEntry is one member of an SBOM bundle as a consumer reads it.
type bundleEntry struct {
	header *tar.Header
	body   string
}

// readBundle reads every member of the gzip-compressed tar at bundleName, in
// archive order.
func readBundle(t *testing.T, bundleName string) []bundleEntry {
	t.Helper()

	file, err := os.Open(bundleName)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = file.Close() }()

	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}

	var entries []bundleEntry

	for reader := tar.NewReader(gz); ; {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}

		entries = append(entries, bundleEntry{header: header, body: string(body)})
	}

	return entries
}

// assertBundleEntries requires the archive to hold exactly these entries. Both
// sides are sorted first: nothing reads these archives in order, so entry order
// is discovery order rather than a contract, and pinning it would fail on a
// harmless change to how SBOMs are found.
func assertBundleEntries(t *testing.T, bundleName string, want []string) {
	t.Helper()

	entries := readBundle(t, bundleName)

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.header.Name)
	}

	slices.Sort(got)

	wantSorted := slices.Clone(want)
	slices.Sort(wantSorted)

	if !slices.Equal(got, wantSorted) {
		t.Errorf("bundle entries = %v, want %v", got, wantSorted)
	}
}
