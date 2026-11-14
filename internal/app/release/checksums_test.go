// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// TestChecksums_HashesReleaseArtifactsUnderTheirBasename pins both halves of a
// manifest line for the release-artifacts directory: the subject is the plain
// basename, and the digest is the file's real SHA-256.
//
// The digests below were produced by sha256sum, not by this package, so a
// regression that emitted something merely 64 characters long would fail here.
func TestChecksums_HashesReleaseArtifactsUnderTheirBasename(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile(filepath.Join("release-artifacts", "app.jar"), []byte("hi"))
	fsys.WriteFile(filepath.Join("release-artifacts", "app.tgz"), []byte("yo"))

	count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{})
	if err != nil {
		t.Fatalf("Checksums: %v", err)
	}

	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	want := map[string]string{
		"app.jar": "8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4", // sha256 of "hi"
		"app.tgz": "e9058ab198f6908f702111b0c0fb5b36f99d00554521886c40e2891b349dc7a1", // sha256 of "yo"
	}

	entries := checksumEntries(t, "checksums.sha256")
	if len(entries) != len(want) {
		t.Fatalf("manifest = %+v, want %d lines", entries, len(want))
	}

	for _, entry := range entries {
		wantDigest, known := want[entry.Subject]
		if !known {
			t.Errorf("unexpected subject %q", entry.Subject)

			continue
		}

		if entry.Digest != wantDigest {
			t.Errorf("%s: digest = %q, want %q", entry.Subject, entry.Digest, wantDigest)
		}
	}
}

func TestChecksums_AttachArtifactsKeepsPath(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile(filepath.Join("extra", "binary-amd64"), []byte("x"))

	count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{
		AttachArtifacts: "extra/*",
	})
	if err != nil {
		t.Fatalf("Checksums: %v", err)
	}

	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	// Exactly one subject, spelled with its directory: files reached through
	// --attach-artifacts keep the path the caller named, unlike the ones
	// discovered under release-artifacts/ which are listed by basename.
	want := []string{"extra/binary-amd64"}
	if got := checksumSubjects(t, "checksums.sha256"); !slices.Equal(got, want) {
		t.Errorf("subjects = %v, want %v", got, want)
	}
}

// TestChecksums_ListsBothSBOMFormats covers SBOMs sitting next to the build.
// Both formats are picked up, under their own names.
func TestChecksums_ListsBothSBOMFormats(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile("my-app-sbom.spdx.json", []byte(`{"spdx":1}`))
	fsys.WriteFile("my-app-sbom.cyclonedx.json", []byte(`{"cdx":1}`))

	count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{})
	if err != nil {
		t.Fatalf("Checksums: %v", err)
	}

	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	want := []string{"my-app-sbom.spdx.json", "my-app-sbom.cyclonedx.json"}
	if got := checksumSubjects(t, "checksums.sha256"); !slices.Equal(got, want) {
		t.Errorf("subjects = %v, want %v", got, want)
	}
}

// TestChecksums_LabelsContainerSBOMsWithoutTheirDirectory covers the SBOM that
// arrives in the scan output directory rather than beside the build. It is
// listed under its basename alone: the manifest sits beside the published
// files, so a subject naming sbom-artifacts/ would not resolve for anyone
// running sha256sum --check against the release.
func TestChecksums_LabelsContainerSBOMsWithoutTheirDirectory(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile(filepath.Join("sbom-artifacts", "my-app-analyzed-container-sbom.spdx.json"), []byte(`{}`))

	count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{})
	if err != nil {
		t.Fatal(err)
	}

	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	want := []string{"my-app-analyzed-container-sbom.spdx.json"}
	if got := checksumSubjects(t, "checksums.sha256"); !slices.Equal(got, want) {
		t.Errorf("subjects = %v, want %v", got, want)
	}
}

// TestChecksums_CreatesEmptyOutputWhenNothingFound covers a run that finds
// nothing to hash. The manifest is still written, empty, because everything
// downstream is entitled to the file existing.
//
// The two rows are the two ways of finding nothing, which are different code
// paths: a directory that is not there returns before the walk begins, while
// one that exists and is empty walks zero entries. The old rows were named for
// that distinction but did not make it -- both ran against a bare temp dir, so
// both took the missing-directory path. The announcement line, printed only
// once the directory has been read, is what tells them apart.
func TestChecksums_CreatesEmptyOutputWhenNothingFound(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		createDirs    bool
		wantAnnounced bool
	}{
		{name: "the directories are not there", createDirs: false, wantAnnounced: false},
		{name: "the directories exist but are empty", createDirs: true, wantAnnounced: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.Chdir()

			if testCase.createDirs {
				fsys.MkdirAll("release-artifacts")
				fsys.MkdirAll("sbom-artifacts")
			}

			var out bytes.Buffer

			count, err := apprelease.Checksums(&out, apprelease.ChecksumsInput{})
			if err != nil {
				t.Fatalf("Checksums: %v", err)
			}

			if count != 0 {
				t.Errorf("count = %d, want 0", count)
			}

			info, err := os.Stat("checksums.sha256")
			if err != nil {
				t.Fatalf("output file should still be created: %v", err)
			}

			// The name of this test is the claim; nothing checked it.
			if info.Size() != 0 {
				t.Errorf("manifest = %d bytes, want empty", info.Size())
			}

			if !strings.Contains(out.String(), "Generated 0 checksums") {
				t.Errorf("out = %q", out.String())
			}

			if announced := strings.Contains(out.String(), "Checksumming release artifacts from"); announced != testCase.wantAnnounced {
				t.Errorf("announced walking the directory = %v, want %v\nout = %q", announced, testCase.wantAnnounced, out.String())
			}
		})
	}
}

// TestChecksums_HonoursACustomOutputPath covers --output: the manifest is
// written where it was asked for, with its contents, and the default path is
// left alone so nothing downstream picks up a stale one.
//
// The nested row deliberately does not create the directory first. Both
// discovery modes share one manifest writer now, so --output some/dir/file
// creates what it needs whether or not --assembly was passed; before that,
// only the assembly path did, and this row had to make the directory itself.
func TestChecksums_HonoursACustomOutputPath(t *testing.T) {
	for name, outputFile := range map[string]string{
		"a plain file name":     "custom-checksums.txt",
		"a path with directory": filepath.Join("output", "checksums.sha256"),
	} {
		t.Run(name, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.Chdir()

			fsys.WriteFile(filepath.Join("release-artifacts", "test.jar"), []byte("test"))

			count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{OutputFile: outputFile})
			if err != nil {
				t.Fatal(err)
			}

			if count != 1 {
				t.Errorf("count = %d, want 1", count)
			}

			// Assert the contents, not just that a file appeared: a manifest
			// created empty at the custom path would satisfy every other
			// check here.
			want := []string{"test.jar"}
			if got := checksumSubjects(t, outputFile); !slices.Equal(got, want) {
				t.Errorf("subjects in %s = %v, want %v", outputFile, got, want)
			}

			if _, err := os.Stat("checksums.sha256"); !os.IsNotExist(err) {
				t.Errorf("default output should not exist when a custom path is used: %v", err)
			}
		})
	}
}

func TestChecksums_CommaSeparatedAttachPatterns(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile("file1.txt", []byte("file1"))
	fsys.WriteFile("file2.md", []byte("file2"))

	count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{AttachArtifacts: "file1.txt,file2.md"})
	if err != nil {
		t.Fatal(err)
	}

	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	want := []string{"file1.txt", "file2.md"}
	if got := checksumSubjects(t, "checksums.sha256"); !slices.Equal(got, want) {
		t.Errorf("subjects = %v, want %v", got, want)
	}
}

// TestChecksums_ExcludesItsOwnOutputFromTheManifest covers the case where the
// manifest is written into the directory it is hashing.
//
// The output file is created before the walk, so a walk over its own directory
// finds it — empty at that point — and records sha256 e3b0c44…, the hash of
// nothing. The manifest then fails `sha256sum --check` against itself the
// moment it is verified, which is the one thing a checksums file exists to
// survive. The guard is in the code and nothing exercised it: every other test
// writes the manifest outside the directories being hashed.
func TestChecksums_ExcludesItsOwnOutputFromTheManifest(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()

	// The manifest lands in the same directory it is told to hash.
	dir := fsys.MkdirAll("release-artifacts")
	fsys.WriteFile(filepath.Join("release-artifacts", "app.jar"), []byte("hi"))

	output := filepath.Join(dir, "checksums.sha256")

	count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{
		ReleaseArtifactsDir: dir,
		OutputFile:          output,
	})
	if err != nil {
		t.Fatalf("Checksums: %v", err)
	}

	if count != 1 {
		t.Errorf("count = %d, want 1: only the artifact, not the manifest", count)
	}

	entries := checksumEntries(t, output)
	for _, entry := range entries {
		if entry.Subject == "checksums.sha256" {
			t.Errorf("the manifest lists itself: %+v", entry)
		}

		// The hash of an empty file is what self-inclusion would record.
		if entry.Digest == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
			t.Errorf("manifest records the empty-file digest for %q", entry.Subject)
		}
	}

	if len(entries) != 1 || entries[0].Subject != "app.jar" {
		t.Errorf("manifest = %+v, want only app.jar", entries)
	}
}

// TestChecksums_ManifestOrderIsDeterministic pins the line order.
//
// A checksums manifest is signed and compared across runs, so two runs over the
// same inputs must produce the same bytes. Directory reads are not ordered by
// the filesystem, and the fixtures elsewhere in this file each hash two files
// and compare them as a map — which cannot see order at all.
func TestChecksums_ManifestOrderIsDeterministic(t *testing.T) {
	names := []string{"zeta.tgz", "alpha.jar", "middle.txt", "beta.zip"}

	render := func(t *testing.T) []string {
		t.Helper()

		fsys := testfs.NewReal(t)
		fsys.Chdir()

		for _, name := range names {
			fsys.WriteFile(filepath.Join("release-artifacts", name), []byte(name))
		}

		if _, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{}); err != nil {
			t.Fatalf("Checksums: %v", err)
		}

		entries := checksumEntries(t, "checksums.sha256")

		subjects := make([]string, 0, len(entries))
		for _, entry := range entries {
			subjects = append(subjects, entry.Subject)
		}

		return subjects
	}

	first := render(t)
	second := render(t)

	if !slices.Equal(first, second) {
		t.Errorf("two runs produced different orders:\n  %v\n  %v", first, second)
	}

	if len(first) != len(names) {
		t.Fatalf("manifest lists %v, want %d entries", first, len(names))
	}

	if !slices.IsSorted(first) {
		t.Errorf("manifest order = %v, want it sorted so a signed manifest is reproducible", first)
	}
}

// Opening the output must not destroy the assembly document or any of its
// files. Hard links and absolute spellings still name the same input.
func TestChecksums_AssemblyRefusesOutputOverlappingInputs(t *testing.T) {
	for _, output := range []string{
		"release-files/app.jar", "release-files/app-sbom.json", "release-files/sboms.zip", "assembly.json",
		"absolute asset", "hard-linked asset", "assembly default",
	} {
		t.Run(output, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.Chdir()
			fsys.WriteFile("release-files/app.jar", []byte("artifact"))
			fsys.WriteFile("release-files/app-sbom.json", []byte("SBOM"))
			fsys.WriteFile("release-files/sboms.zip", []byte("ZIP"))

			assembly := domainrelease.Assembly{
				Version:      domainrelease.AssemblyVersion,
				Assets:       []domainrelease.AssemblyFile{{Path: "release-files/app.jar", Name: "app.jar"}},
				SBOMs:        []domainrelease.AssemblyFile{{Path: "release-files/app-sbom.json", Name: "app-sbom.json"}},
				ChecksumFile: "release-files/checksums.sha256",
				SBOMZipFile:  "release-files/sboms.zip",
			}

			switch output {
			case "absolute asset":
				output = fsys.Path("release-files/app.jar")
			case "hard-linked asset":
				output = "release-files/checksums.sha256"
				if err := os.Link("release-files/app.jar", output); err != nil {
					t.Fatal(err)
				}
			case "assembly default":
				assembly.ChecksumFile, output = "release-files/app.jar", ""
			}

			writeAssembly(t, "assembly.json", assembly)

			before := map[string]string{}

			for _, path := range []string{"assembly.json", "release-files/app.jar", "release-files/app-sbom.json", "release-files/sboms.zip"} {
				before[path] = string(fsys.ReadFile(path))
			}

			count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{AssemblyFile: "assembly.json", OutputFile: output})
			if count != 0 || !errors.Is(err, errs.ErrValidation) {
				t.Errorf("Checksums = %d, %v; want zero, ErrValidation", count, err)
			}

			for path, want := range before {
				if body := fsys.ReadFile(path); string(body) != want {
					t.Errorf("input %s = %q; want unchanged %q", path, body, want)
				}
			}
		})
	}
}

func TestChecksums_AssemblyCanRegenerateItsManifest(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile("release-files/app.jar", []byte("hi"))
	fsys.WriteFile("release-files/checksums.sha256", []byte(strings.Repeat("stale", 50)))
	writeAssembly(t, "assembly.json", domainrelease.Assembly{
		Version:      domainrelease.AssemblyVersion,
		Assets:       []domainrelease.AssemblyFile{{Path: "release-files/app.jar", Name: "app.jar"}},
		ChecksumFile: "release-files/checksums.sha256",
	})

	for range 2 {
		count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{AssemblyFile: "assembly.json"})
		if err != nil || count != 1 {
			t.Fatalf("Checksums = %d, %v; want 1, nil", count, err)
		}

		body, err := os.ReadFile("release-files/checksums.sha256")

		const want = "8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4  app.jar\n"

		if err != nil || string(body) != want {
			t.Fatalf("manifest = %q, %v; want %q", body, err, want)
		}
	}
}

// Every discovery route and assembly labels reach the same line writer.
func TestChecksums_RefusesLineBreaksInLabels(t *testing.T) {
	for kind, newline := range map[string]string{"LF": "\n", "CR": "\r"} {
		for _, source := range []string{"release", "attach", "container", "workdir", "assembly"} {
			t.Run(source+"/"+kind, func(t *testing.T) {
				fsys := testfs.NewReal(t)
				fsys.Chdir()

				input := apprelease.ChecksumsInput{}
				name := "app" + newline + "injected"

				switch source {
				case "release":
					fsys.WriteFile("release-artifacts/"+name, []byte("artifact"))
				case "attach":
					fsys.WriteFile("extra/"+name, []byte("artifact"))

					input.AttachArtifacts = "extra/*"
				case "container":
					fsys.WriteFile("sbom-artifacts/"+name+"-analyzed-container-sbom.spdx.json", []byte("{}"))
				case "workdir":
					fsys.WriteFile(name+"-sbom.spdx.json", []byte("{}"))
				case "assembly":
					fsys.WriteFile("release-files/app.jar", []byte("artifact"))

					input.AssemblyFile = "assembly.json"
					writeAssembly(t, input.AssemblyFile, domainrelease.Assembly{
						Version: domainrelease.AssemblyVersion,
						Assets:  []domainrelease.AssemblyFile{{Path: "release-files/app.jar", Name: name}},
					})
				}

				count, err := apprelease.Checksums(&bytes.Buffer{}, input)
				if count != 0 || !errors.Is(err, errs.ErrValidation) {
					t.Fatalf("Checksums = %d, %v; want zero, ErrValidation", count, err)
				}

				body, err := os.ReadFile("checksums.sha256")
				if err != nil || len(body) != 0 {
					t.Fatalf("refused label reached manifest: %q, %v", body, err)
				}
			})
		}
	}
}

func TestChecksums_AcceptsSpacesAndBackslashesInLabels(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()

	want := []string{"ordinary file.jar", `windows\name.jar`}

	for _, name := range want {
		fsys.WriteFile(filepath.Join("release-artifacts", name), []byte("artifact"))
	}

	if _, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{}); err != nil {
		t.Fatal(err)
	}

	if got := checksumSubjects(t, "checksums.sha256"); !slices.Equal(got, want) {
		t.Errorf("subjects = %q, want %q", got, want)
	}
}
