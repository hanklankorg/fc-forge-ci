// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func TestValidateAssemblyAndSignerAdmission(t *testing.T) {
	const (
		asset     = "release-files/assets/app.tgz"
		bundle    = "release-files/assets/app-sboms.tar.gz"
		sbom      = "release-files/sboms/app.spdx.json"
		checksums = "release-files/checksums.sha256"
	)

	row := func(body, name string) string { return fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(body)), name) }
	validRows := row("archive", "app.tgz") + row("sbom archive", "app-sboms.tar.gz")

	cases := []struct {
		name   string
		mutate func(*testing.T, *testfs.Real, *domainrelease.Assembly)
		want   string
	}{
		{name: "valid"},
		{name: "corrupted asset", mutate: func(_ *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
			fs.WriteFile(asset, []byte("corrupted"))
		}, want: "digest mismatch"},
		{name: "stale checksum", mutate: func(_ *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
			fs.WriteFile(checksums, []byte(row("old archive", "app.tgz")+row("sbom archive", "app-sboms.tar.gz")))
		}, want: "digest mismatch"},
		{name: "missing checksum row", mutate: func(_ *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
			fs.WriteFile(checksums, []byte(row("archive", "app.tgz")))
		}, want: "missing public release assets"},
		{name: "extra checksum row", mutate: func(_ *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
			fs.WriteFile(checksums, []byte(validRows+row("extra", "extra.tgz")))
		}, want: "not public release assets"},
		{name: "duplicate checksum row", mutate: func(_ *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
			fs.WriteFile(checksums, []byte(validRows+row("archive", "app.tgz")))
		}, want: "duplicate subjects"},
		{name: "malformed checksum", mutate: func(_ *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
			fs.WriteFile(checksums, []byte("not a checksum\n"))
		}, want: "malformed checksum"},
		{name: "missing asset", mutate: func(t *testing.T, _ *testfs.Real, _ *domainrelease.Assembly) {
			t.Helper()

			removeAssemblyInput(t, asset)
		}, want: "missing"},
		{name: "missing SBOM bundle", mutate: func(t *testing.T, _ *testfs.Real, _ *domainrelease.Assembly) {
			t.Helper()

			removeAssemblyInput(t, bundle)
		}, want: "SBOM bundle is missing"},
		{name: "missing SBOM input", mutate: func(t *testing.T, _ *testfs.Real, _ *domainrelease.Assembly) {
			t.Helper()

			removeAssemblyInput(t, sbom)
		}, want: "missing"},
		{name: "missing checksums", mutate: func(t *testing.T, _ *testfs.Real, _ *domainrelease.Assembly) {
			t.Helper()

			removeAssemblyInput(t, checksums)
		}, want: "missing"},
		{name: "absent checksum declaration", mutate: func(_ *testing.T, _ *testfs.Real, asm *domainrelease.Assembly) {
			asm.ChecksumFile = ""
		}, want: "checksums file is required"},
		{name: "empty checksums", mutate: func(_ *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
			fs.WriteFile(checksums, nil)
		}, want: "no subjects"},
		{name: "duplicate public basename", mutate: func(_ *testing.T, _ *testfs.Real, asm *domainrelease.Assembly) {
			asm.Assets = append(asm.Assets, asm.Assets[0])
		}, want: "duplicate name/path"},
		{name: "relabeled asset", mutate: func(_ *testing.T, _ *testfs.Real, asm *domainrelease.Assembly) {
			asm.Assets[0].Name = "another.tgz"
		}, want: "inconsistent"},
		{name: "reserved output declared as asset", mutate: func(_ *testing.T, fs *testfs.Real, asm *domainrelease.Assembly) {
			fs.WriteFile("release-files/assets/unrelated.asc", []byte("untrusted signature"))

			asm.Assets = append(asm.Assets, domainrelease.AssemblyFile{Path: "release-files/assets/unrelated.asc", Name: "unrelated.asc"})
		}, want: "reserved signer output"},
		{name: "preexisting GPG output", mutate: func(_ *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
			fs.WriteFile(asset+".asc", []byte("untrusted signature"))
		}, want: "reserved signer output"},
		{name: "preexisting Sigstore output", mutate: func(_ *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
			fs.WriteFile(checksums+".bundle", []byte("untrusted bundle"))
		}, want: "reserved signer output"},
		{name: "dangling sidecar symlink", mutate: func(t *testing.T, _ *testfs.Real, _ *domainrelease.Assembly) {
			t.Helper()

			if err := os.Symlink("missing", bundle+".bundle"); err != nil {
				t.Fatal(err)
			}
		}, want: "reserved signer output"},
		{name: "linked asset", mutate: func(t *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
			t.Helper()

			removeAssemblyInput(t, asset)
			fs.WriteFile("other.tgz", []byte("archive"))

			if err := os.Symlink("../../other.tgz", asset); err != nil {
				t.Fatal(err)
			}
		}, want: "symlink"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := testfs.NewReal(t)
			fs.Chdir()
			fs.WriteFile(asset, []byte("archive"))
			fs.WriteFile(bundle, []byte("sbom archive"))
			fs.WriteFile(sbom, []byte(`{}`))
			fs.WriteFile(checksums, []byte(validRows))

			asm := domainrelease.Assembly{
				Version:      domainrelease.AssemblyVersion,
				Assets:       []domainrelease.AssemblyFile{{Path: asset, Name: filepath.Base(asset), Required: true}},
				SBOMs:        []domainrelease.AssemblyFile{{Path: sbom, Name: filepath.Base(sbom)}},
				ChecksumFile: checksums, SBOMBundleFile: bundle,
			}
			if tc.mutate != nil {
				tc.mutate(t, fs, &asm)
			}

			writeAssembly(t, domainrelease.DefaultAssemblyFile, asm)

			check := func(err error) {
				t.Helper()

				if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("error = %v, want %q", err, tc.want)
				}
			}
			check(apprelease.ValidateAssembly(domainrelease.DefaultAssemblyFile))

			signer := &fakeSigner{}
			check(apprelease.SignArtifacts(context.Background(), signer, io.Discard, apprelease.SignInput{AssemblyFile: domainrelease.DefaultAssemblyFile}))

			if tc.want != "" && len(signer.signed) != 0 {
				t.Fatalf("refused assembly reached signer: %v", signer.signed)
			}

			if tc.want == "" && len(signer.signed) != 3 {
				t.Fatalf("valid assembly signed %v, want asset, bundle and checksums", signer.signed)
			}
		})
	}
}

func removeAssemblyInput(t *testing.T, path string) {
	t.Helper()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAssemblyEmptyFileRelease(t *testing.T) {
	fs := testfs.NewReal(t)
	fs.Chdir()
	fs.WriteFile("release-files/checksums.sha256", nil)
	writeAssembly(t, domainrelease.DefaultAssemblyFile, domainrelease.Assembly{
		Version: domainrelease.AssemblyVersion, ChecksumFile: "release-files/checksums.sha256",
		SBOMBundleFile: "release-files/assets/unused-sboms.tar.gz",
	})

	if err := apprelease.ValidateAssembly(domainrelease.DefaultAssemblyFile); err != nil {
		t.Fatal(err)
	}

	signer := &fakeSigner{}
	if err := apprelease.SignArtifacts(context.Background(), signer, io.Discard, apprelease.SignInput{AssemblyFile: domainrelease.DefaultAssemblyFile}); err != nil {
		t.Fatal(err)
	}

	if len(signer.signed) != 0 {
		t.Fatalf("empty assembly signed %v", signer.signed)
	}
}

// The provenance statement is the signing job's own file: it writes it from
// the checksums it admitted and signs it with the release files. An unsigned
// assembly that brings one is refused before it is signed, a release file may
// not take its name, and a release without files has no statement.
func TestAssemblyProvenanceStatement(t *testing.T) {
	const (
		asset     = "release-files/assets/app.tgz"
		checksums = "release-files/checksums.sha256"
		statement = "release-files/slsa-provenance.intoto.json"
	)

	row := func(body, name string) string { return fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(body)), name) }

	assembly := func(t *testing.T, rows string, assets ...string) *testfs.Real {
		t.Helper()

		fs := testfs.NewReal(t)
		fs.Chdir()
		fs.WriteFile(checksums, []byte(rows))

		asm := domainrelease.Assembly{Version: domainrelease.AssemblyVersion, ChecksumFile: checksums, ProvenanceFile: statement}

		for _, path := range assets {
			fs.WriteFile(path, []byte("archive"))

			asm.Assets = append(asm.Assets, domainrelease.AssemblyFile{Path: path, Name: filepath.Base(path), Required: true})
		}

		writeAssembly(t, domainrelease.DefaultAssemblyFile, asm)

		return fs
	}

	t.Run("an assembly that brings a statement is refused", func(t *testing.T) {
		fs := assembly(t, row("archive", "app.tgz"), asset)
		fs.WriteFile(statement, []byte(`{"planted":true}`))

		_, _, provenanceErr := apprelease.AssemblyProvenance(domainrelease.DefaultAssemblyFile)

		for name, err := range map[string]error{
			"validate-assembly": apprelease.ValidateAssembly(domainrelease.DefaultAssemblyFile),
			"provenance":        provenanceErr,
		} {
			if err == nil || !strings.Contains(err.Error(), "reserved signer output") {
				t.Errorf("%s: error = %v, want the statement refused as a reserved signer output", name, err)
			}
		}
	})

	t.Run("a release file may not have the statement's name", func(t *testing.T) {
		taken := "release-files/assets/slsa-provenance.intoto.json"
		assembly(t, row("archive", "app.tgz")+row("archive", filepath.Base(taken)), asset, taken)

		err := apprelease.ValidateAssembly(domainrelease.DefaultAssemblyFile)
		if err == nil || !strings.Contains(err.Error(), "has the name of the provenance statement") {
			t.Fatalf("error = %v, want the release file's name refused", err)
		}
	})

	t.Run("the statement has one place, beside the checksums file", func(t *testing.T) {
		assembly(t, row("archive", "app.tgz"), asset)
		writeAssembly(t, domainrelease.DefaultAssemblyFile, domainrelease.Assembly{
			Version:        domainrelease.AssemblyVersion,
			Assets:         []domainrelease.AssemblyFile{{Path: asset, Name: "app.tgz", Required: true}},
			ChecksumFile:   checksums,
			ProvenanceFile: "release-notes.md",
		})

		err := apprelease.ValidateAssembly(domainrelease.DefaultAssemblyFile)
		if err == nil || !strings.Contains(err.Error(), `assembly provenance file must be "`+statement+`"`) {
			t.Fatalf("error = %v, want the other place refused", err)
		}
	})

	t.Run("a statement with a signature already beside it is not signed", func(t *testing.T) {
		fs := assembly(t, row("archive", "app.tgz"), asset)
		fs.WriteFile(statement, []byte(`{}`))
		fs.WriteFile(statement+".bundle", []byte("untrusted bundle"))

		signer := &fakeSigner{}

		err := apprelease.SignArtifacts(context.Background(), signer, io.Discard, apprelease.SignInput{AssemblyFile: domainrelease.DefaultAssemblyFile})
		if err == nil || !strings.Contains(err.Error(), "reserved signer output") || len(signer.signed) != 0 {
			t.Fatalf("error = %v, signed %v; want the bundle refused and nothing signed", err, signer.signed)
		}
	})

	t.Run("a release without files has no statement", func(t *testing.T) {
		assembly(t, "")

		body, path, err := apprelease.AssemblyProvenance(domainrelease.DefaultAssemblyFile)
		if err != nil || len(body) != 0 || path != statement {
			t.Fatalf("checksums %q, path %q, error %v; want no checksums and no error", body, path, err)
		}
	})

	t.Run("an assembly that names no statement is refused", func(t *testing.T) {
		fs := testfs.NewReal(t)
		fs.Chdir()
		fs.WriteFile(checksums, nil)
		writeAssembly(t, domainrelease.DefaultAssemblyFile, domainrelease.Assembly{Version: domainrelease.AssemblyVersion, ChecksumFile: checksums})

		if _, _, err := apprelease.AssemblyProvenance(domainrelease.DefaultAssemblyFile); err == nil || !strings.Contains(err.Error(), "names no provenance file") {
			t.Fatalf("error = %v, want the missing provenance file named", err)
		}
	})
}
