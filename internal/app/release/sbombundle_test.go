// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

type fakeSBOMSigner struct {
	signed []string
}

type sidecarSBOMSigner struct {
	advertised []string
	written    string
	content    []byte
}

func (s sidecarSBOMSigner) Extensions() []string { return s.advertised }

func (s sidecarSBOMSigner) SignFile(_ context.Context, file string) error {
	if s.written == "" {
		return nil
	}

	return os.WriteFile(file+s.written, s.content, 0o600)
}

func (f *fakeSBOMSigner) Extensions() []string { return []string{".asc"} }

func (f *fakeSBOMSigner) SignFile(_ context.Context, file string) error {
	f.signed = append(f.signed, file)

	return os.WriteFile(file+".asc", []byte("sig"), 0o644)
}

func TestCreateSBOMBundle_NoSBOMsIsNoOp(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()

	res, err := apprelease.CreateSBOMBundle(context.Background(), nil, apprelease.SBOMBundleInput{
		ProjectName: "x", Version: "1.0.0",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	if res.BundleName != "" || res.EntryCount != 0 {
		t.Errorf("expected empty result for no-SBOMs, got %+v", res)
	}

	// And no archive was left behind. An empty bundle published as a release
	// asset is worse than none: a consumer downloads it expecting SBOMs.
	entries, err := os.ReadDir(fsys.Root)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Errorf("a run with no SBOMs created %v", entries)
	}
}

// TestCreateSBOMBundle_FlattensSBOMsFoundInDifferentPlaces varies where an SBOM
// was left: two beside the build and one in the container-scan output
// directory. All three go into the archive, and the nested one loses its
// directory prefix — a consumer unpacking the bundle gets a flat set of SBOMs,
// not a sbom-artifacts/ tree.
func TestCreateSBOMBundle_FlattensSBOMsFoundInDifferentPlaces(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile("my-app-sbom.spdx.json", []byte(`{"spdx":1}`))
	fsys.WriteFile("my-app-sbom.cyclonedx.json", []byte(`{"cdx":1}`))
	fsys.WriteFile(filepath.Join("sbom-artifacts", "my-app-analyzed-container-linux-amd64-sbom.spdx.json"), []byte(`{}`))

	res, err := apprelease.CreateSBOMBundle(context.Background(), nil, apprelease.SBOMBundleInput{
		ProjectName: "my-app", Version: "1.2.3",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	if res.BundleName != "my-app-1.2.3-sboms.tar.gz" {
		t.Errorf("bundle name = %q", res.BundleName)
	}

	if res.EntryCount != 3 {
		t.Errorf("entries = %d, want 3", res.EntryCount)
	}

	assertBundleEntries(t, res.BundleName, []string{
		"my-app-analyzed-container-linux-amd64-sbom.spdx.json", // flattened: no sbom-artifacts/ prefix
		"my-app-sbom.cyclonedx.json",
		"my-app-sbom.spdx.json",
	})
}

func TestCreateSBOMBundle_VersionedBundleName(t *testing.T) {
	tests := []struct {
		name        string
		projectName string
		version     string
		fixture     string
		want        string
	}{
		{name: "strips_v_prefix", projectName: "x", version: "v2.0.0", fixture: "x-sbom.spdx.json", want: "x-2.0.0-sboms.tar.gz"},
		{name: "keeps_prerelease_suffix", projectName: "myapp", version: "1.0.0-SNAPSHOT", fixture: "myapp-pom-sbom.spdx.json", want: "myapp-1.0.0-SNAPSHOT-sboms.tar.gz"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.Chdir()
			fsys.WriteFile(testCase.fixture, []byte(`{}`))

			res, err := apprelease.CreateSBOMBundle(context.Background(), nil, apprelease.SBOMBundleInput{
				ProjectName: testCase.projectName,
				Version:     testCase.version,
			}, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}

			if res.BundleName != testCase.want {
				t.Errorf("bundle name = %q, want %q", res.BundleName, testCase.want)
			}
		})
	}
}

// TestCreateSBOMBundle_RecognisesEverySBOMNamingConvention varies the filename
// instead of the location: the SPDX one a build tool writes from the POM, the
// CycloneDX one from analysing the built jar, and the SPDX one from analysing
// a tar archive. Each producer names its output differently and all three must
// be recognised as SBOMs.
func TestCreateSBOMBundle_RecognisesEverySBOMNamingConvention(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile("myapp-pom-sbom.spdx.json", []byte(`{"spdxVersion":"2.3"}`))
	fsys.WriteFile("myapp-analyzed-jar-sbom.cyclonedx.json", []byte(`{"bomFormat":"CycloneDX"}`))
	fsys.WriteFile("myapp-analyzed-tararchive-sbom.spdx.json", []byte(`{"spdx":"tararchive"}`))

	res, err := apprelease.CreateSBOMBundle(context.Background(), nil, apprelease.SBOMBundleInput{
		ProjectName: "myapp", Version: "1.0.0",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	// Exactly these: the old check only looked for each wanted name and so
	// could not notice a fourth file being swept into a published archive.
	assertBundleEntries(t, res.BundleName, []string{
		"myapp-pom-sbom.spdx.json",
		"myapp-analyzed-jar-sbom.cyclonedx.json",
		"myapp-analyzed-tararchive-sbom.spdx.json",
	})
}

// TestCreateSBOMBundle_ScopesDiscoveryToTheProjectName is the negative control
// for the discovery scope: an SBOM another project (or a stray tool) left in
// the working directory or the container-SBOM dir must not be swept into this
// project's published, signed archive.
func TestCreateSBOMBundle_ScopesDiscoveryToTheProjectName(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile("my-app-sbom.spdx.json", []byte(`{}`))
	fsys.WriteFile("other-app-sbom.spdx.json", []byte(`{}`))
	fsys.WriteFile(filepath.Join("sbom-artifacts", "my-app-analyzed-container-linux-amd64-sbom.spdx.json"), []byte(`{}`))
	fsys.WriteFile(filepath.Join("sbom-artifacts", "other-app-analyzed-container-linux-amd64-sbom.spdx.json"), []byte(`{}`))

	res, err := apprelease.CreateSBOMBundle(context.Background(), nil, apprelease.SBOMBundleInput{
		ProjectName: "my-app", Version: "1.2.3",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	assertBundleEntries(t, res.BundleName, []string{
		"my-app-sbom.spdx.json",
		"my-app-analyzed-container-linux-amd64-sbom.spdx.json",
	})
}

func TestCreateSBOMBundle_SigningRequestedCreatesAsc(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	dir := fsys.Root
	fsys.WriteFile("myapp-pom-sbom.spdx.json", []byte(`{}`))

	signer := &fakeSBOMSigner{}

	var out bytes.Buffer

	res, err := apprelease.CreateSBOMBundle(context.Background(), signer, apprelease.SBOMBundleInput{
		ProjectName:   "myapp",
		Version:       "v1.2.3",
		SignArtifacts: true,
	}, &out)
	if err != nil {
		t.Fatal(err)
	}

	if !res.Signed {
		t.Fatal("expected Signed=true")
	}

	if len(signer.signed) != 1 || signer.signed[0] != "myapp-1.2.3-sboms.tar.gz" {
		t.Errorf("signed = %v", signer.signed)
	}

	if _, err := os.Stat(filepath.Join(dir, "myapp-1.2.3-sboms.tar.gz.asc")); err != nil {
		t.Errorf("bundle signature missing: %v", err)
	}

	if !strings.Contains(out.String(), "Signed SBOM bundle") {
		t.Errorf("output = %q", out.String())
	}
}

func TestCreateSBOMBundle_SigningSkippedWithoutFlag(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	dir := fsys.Root
	fsys.WriteFile("myapp-pom-sbom.spdx.json", []byte(`{}`))

	signer := &fakeSBOMSigner{}

	res, err := apprelease.CreateSBOMBundle(context.Background(), signer, apprelease.SBOMBundleInput{
		ProjectName: "myapp", Version: "v1.2.3", SignArtifacts: false,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	if res.Signed {
		t.Fatalf("expected Signed=false")
	}

	if len(signer.signed) != 0 {
		t.Errorf("signer should not be called, got %v", signer.signed)
	}

	if _, err := os.Stat(filepath.Join(dir, "myapp-1.2.3-sboms.tar.gz.asc")); !os.IsNotExist(err) {
		t.Errorf("bundle signature should not exist: %v", err)
	}
}

func TestCreateSBOMBundle_SigningRejectsMissingEmptyAndWrongSidecars(t *testing.T) {
	tests := []struct {
		name   string
		signer sidecarSBOMSigner
	}{
		{name: "missing", signer: sidecarSBOMSigner{advertised: []string{".asc"}}},
		{name: "empty", signer: sidecarSBOMSigner{advertised: []string{".asc"}, written: ".asc"}},
		{name: "wrong extension", signer: sidecarSBOMSigner{advertised: []string{".sigstore.json"}, written: ".asc", content: []byte("sig")}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fsys := testfs.NewReal(t)
			fsys.Chdir()
			fsys.WriteFile("myapp-pom-sbom.spdx.json", []byte(`{}`))

			_, err := apprelease.CreateSBOMBundle(context.Background(), testCase.signer, apprelease.SBOMBundleInput{
				ProjectName: "myapp", Version: "v1.2.3", SignArtifacts: true,
			}, &bytes.Buffer{})
			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("error = %v, want ErrValidation", err)
			}
		})
	}
}

func TestCreateSBOMBundle_SigningRemovesStaleSidecarBeforeBackend(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile("myapp-pom-sbom.spdx.json", []byte(`{}`))
	fsys.WriteFile("myapp-1.2.3-sboms.tar.gz.asc", []byte("stale"))

	_, err := apprelease.CreateSBOMBundle(context.Background(), sidecarSBOMSigner{advertised: []string{".asc"}}, apprelease.SBOMBundleInput{
		ProjectName: "myapp", Version: "v1.2.3", SignArtifacts: true,
	}, &bytes.Buffer{})
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}

	if _, statErr := os.Stat("myapp-1.2.3-sboms.tar.gz.asc"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale sidecar survived signing attempt: %v", statErr)
	}
}

func TestCreateSBOMBundle_SigningUsesAdvertisedExtension(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()
	fsys.WriteFile("myapp-pom-sbom.spdx.json", []byte(`{}`))

	var out bytes.Buffer

	res, err := apprelease.CreateSBOMBundle(context.Background(), sidecarSBOMSigner{
		advertised: []string{".sigstore.json"}, written: ".sigstore.json", content: []byte("bundle"),
	}, apprelease.SBOMBundleInput{
		ProjectName: "myapp", Version: "v1.2.3", SignArtifacts: true,
	}, &out)
	if err != nil {
		t.Fatal(err)
	}

	if !res.Signed {
		t.Fatal("expected Signed=true")
	}

	if _, statErr := os.Stat("myapp-1.2.3-sboms.tar.gz.sigstore.json"); statErr != nil {
		t.Fatalf("advertised sidecar missing: %v", statErr)
	}

	if strings.Contains(out.String(), ".asc") {
		t.Fatalf("signing output assumes an OpenPGP sidecar: %q", out.String())
	}
}

// TestCreateSBOMBundle_IsReproducibleAndHoldsExactlyItsInputs pins the bundle
// as a release asset someone else can rebuild: the same SBOMs give the same
// bytes whatever mode and time the staged files carry, and the archive holds
// the given entries, in the given order, with their contents.
func TestCreateSBOMBundle_IsReproducibleAndHoldsExactlyItsInputs(t *testing.T) {
	fsys := testfs.NewReal(t)
	fsys.Chdir()

	sboms := []struct{ path, name, body string }{
		{"staged/zeta-sbom.spdx.json", "zeta-sbom.spdx.json", `{"spdxVersion":"SPDX-2.3"}`},
		{"staged/alpha-sbom.cyclonedx.json", "alpha-sbom.cyclonedx.json", `{"bomFormat":"CycloneDX"}`},
	}

	const bundlePath = "release-files/app-1.0.0-sboms.tar.gz"

	asm := domainrelease.Assembly{Version: domainrelease.AssemblyVersion, SBOMBundleFile: bundlePath}

	for _, sbom := range sboms {
		fsys.WriteFile(sbom.path, []byte(sbom.body))
		asm.SBOMs = append(asm.SBOMs, domainrelease.AssemblyFile{Path: sbom.path, Name: sbom.name, Required: true})
	}

	writeAssembly(t, "assembly.json", asm)

	write := func() []byte {
		t.Helper()

		res, err := apprelease.CreateSBOMBundle(context.Background(), nil, apprelease.SBOMBundleInput{AssemblyFile: "assembly.json"}, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}

		if res.BundleName != bundlePath || res.EntryCount != len(sboms) {
			t.Fatalf("SBOM bundle result = %+v", res)
		}

		body, err := os.ReadFile(bundlePath)
		if err != nil {
			t.Fatal(err)
		}

		return body
	}

	first := write()

	// Nothing about the staged file but its bytes may reach the bundle.
	for _, sbom := range sboms {
		if err := os.Chmod(sbom.path, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.Chtimes(sbom.path, time.Unix(1_700_000_000, 0), time.Unix(1_700_000_000, 0)); err != nil {
			t.Fatal(err)
		}
	}

	if second := write(); !bytes.Equal(first, second) {
		t.Error("two bundles of the same SBOMs differ")
	}

	entries := readBundle(t, bundlePath)
	if len(entries) != len(sboms) {
		t.Fatalf("bundle holds %d entries, want %d", len(entries), len(sboms))
	}

	for i, sbom := range sboms {
		header := entries[i].header
		if header.Name != sbom.name || entries[i].body != sbom.body {
			t.Errorf("entry %d = %q %q, want %q %q", i, header.Name, entries[i].body, sbom.name, sbom.body)
		}

		if header.Typeflag != tar.TypeReg || header.Mode != 0o644 || !header.ModTime.Equal(time.Unix(0, 0)) ||
			header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" || header.Format != tar.FormatUSTAR {
			t.Errorf("entry %s carries host metadata: %+v", sbom.name, header)
		}
	}
}
