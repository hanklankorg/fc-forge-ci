// SPDX-FileCopyrightText: The Forge CI Authors
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
	const asset = "release-files/assets/app.tgz"
	const zip = "release-files/assets/app-sboms.zip"
	const sbom = "release-files/sboms/app.spdx.json"
	const checksums = "release-files/checksums.sha256"
	row := func(body, name string) string { return fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(body)), name) }
	validRows := row("archive", "app.tgz") + row("sbom archive", "app-sboms.zip")
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
			fs.WriteFile(checksums, []byte(row("old archive", "app.tgz")+row("sbom archive", "app-sboms.zip")))
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
			removeAssemblyInput(t, asset)
		}, want: "missing"},
		{name: "missing SBOM ZIP", mutate: func(t *testing.T, _ *testfs.Real, _ *domainrelease.Assembly) {
			removeAssemblyInput(t, zip)
		}, want: "SBOM ZIP is missing"},
		{name: "missing SBOM input", mutate: func(t *testing.T, _ *testfs.Real, _ *domainrelease.Assembly) {
			removeAssemblyInput(t, sbom)
		}, want: "missing"},
		{name: "missing checksums", mutate: func(t *testing.T, _ *testfs.Real, _ *domainrelease.Assembly) {
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
			if err := os.Symlink("missing", zip+".bundle"); err != nil {
				t.Fatal(err)
			}
		}, want: "reserved signer output"},
		{name: "linked asset", mutate: func(t *testing.T, fs *testfs.Real, _ *domainrelease.Assembly) {
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
			fs.WriteFile(zip, []byte("sbom archive"))
			fs.WriteFile(sbom, []byte(`{}`))
			fs.WriteFile(checksums, []byte(validRows))
			asm := domainrelease.Assembly{
				Version:      domainrelease.AssemblyVersion,
				Assets:       []domainrelease.AssemblyFile{{Path: asset, Name: filepath.Base(asset), Required: true}},
				SBOMs:        []domainrelease.AssemblyFile{{Path: sbom, Name: filepath.Base(sbom)}},
				ChecksumFile: checksums, SBOMZipFile: zip,
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
				t.Fatalf("valid assembly signed %v, want asset, ZIP and checksums", signer.signed)
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
		SBOMZipFile: "release-files/assets/unused-sboms.zip",
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
