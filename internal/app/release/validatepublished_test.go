// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
)

// forgeRelease is a forge holding at most one release for the tag asked for.
type forgeRelease struct{ release *provider.ListedRelease }

func (f forgeRelease) ReadRelease(context.Context, string, string) (*provider.ListedRelease, error) {
	return f.release, nil
}

func holding(assets ...provider.ListedAsset) forgeRelease {
	return forgeRelease{release: &provider.ListedRelease{Tag: "v1.0.0", Assets: assets}}
}

func sha256Of(content string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
}

// The planned files are an archive of 7 bytes and its bundle of 6. Each case
// is what the forge answers for them.
func TestValidatePublishedRelease_ComparesTheForgeWithThePlannedFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive, bundle := filepath.Join(dir, "app.tar.gz"), filepath.Join(dir, "app.tar.gz.bundle")
	mustWrite(t, archive, "archive")
	mustWrite(t, bundle, "bundle")

	whole := []provider.ListedAsset{{Name: "app.tar.gz", Size: 7}, {Name: "app.tar.gz.bundle", Size: 6}}

	cases := []struct {
		name     string
		forge    forgeRelease
		draft    bool
		wantErr  []string // empty: the command succeeds
		wantWarn string
	}{
		{name: "every planned file is there", forge: holding(whole...)},
		{name: "a forge that reports no size", forge: holding(provider.ListedAsset{Name: "app.tar.gz", SizeUnknown: true}, provider.ListedAsset{Name: "app.tar.gz.bundle", SizeUnknown: true})},
		{name: "a lost file and a lost signature are both named", forge: holding(), wantErr: []string{"missing app.tar.gz;", "missing app.tar.gz.bundle"}},
		{name: "a lost signature", forge: holding(whole[0]), wantErr: []string{"missing app.tar.gz.bundle"}},
		{name: "a truncated file", forge: holding(provider.ListedAsset{Name: "app.tar.gz", Size: 3}, whole[1]), wantErr: []string{"app.tar.gz is 3 bytes, planned 7"}},
		{name: "a file truncated to nothing", forge: holding(provider.ListedAsset{Name: "app.tar.gz"}, whole[1]), wantErr: []string{"app.tar.gz is 0 bytes, planned 7"}},
		{name: "the digest the forge reports matches", forge: holding(provider.ListedAsset{Name: "app.tar.gz", Size: 7, Digest: sha256Of("archive")}, whole[1])},
		{name: "other bytes of the same size", forge: holding(provider.ListedAsset{Name: "app.tar.gz", Size: 7, Digest: sha256Of("swapped")}, whole[1]), wantErr: []string{"app.tar.gz has digest " + sha256Of("swapped") + " on the forge, planned " + sha256Of("archive")}},
		{name: "a digest of another kind verifies nothing", forge: holding(provider.ListedAsset{Name: "app.tar.gz", Size: 7, Digest: "sha512:00"}, whole[1]), wantErr: []string{`app.tar.gz has no SHA-256 digest on the forge (it reports "sha512:00")`}},
		{name: "no digest where the forge computes one", forge: holding(provider.ListedAsset{Name: "app.tar.gz", Size: 7, DigestExpected: true}, whole[1]), wantErr: []string{`app.tar.gz has no SHA-256 digest on the forge (it reports "")`, "was not verified"}},
		{name: "a file the plan does not name warns", forge: holding(slices.Concat(whole, []provider.ListedAsset{{Name: "notes.pdf", Size: 1}})...), wantWarn: "holds notes.pdf"},
		{name: "no release for the tag", wantErr: []string{"no release v1.0.0 is visible"}},
		{name: "a draft that is not visible was not verified", draft: true, wantErr: []string{"no draft release v1.0.0 is visible"}},
		{name: "a draft that holds every planned file", forge: forgeRelease{release: &provider.ListedRelease{Tag: "v1.0.0", Draft: true, Assets: whole}}, draft: true},
		{name: "a draft the token can see is compared", forge: forgeRelease{release: &provider.ListedRelease{Tag: "v1.0.0", Draft: true}}, draft: true, wantErr: []string{"missing app.tar.gz;"}},
		{name: "a release left a draft", forge: forgeRelease{release: &provider.ListedRelease{Tag: "v1.0.0", Draft: true, Assets: whole}}, wantErr: []string{"still a draft"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out, warnings bytes.Buffer

			err := apprelease.ValidatePublishedRelease(t.Context(), tc.forge, &out,
				output.NewAnnotator(&warnings, output.FormatText), apprelease.ValidatePublishedInput{
					Tag: "v1.0.0", Repository: "owner/app", Draft: tc.draft, Assets: []string{archive, bundle},
				})

			if len(tc.wantErr) == 0 && err != nil {
				t.Fatalf("err = %v, want success", err)
			}

			for _, want := range tc.wantErr {
				if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want a validation error naming %q", err, want)
				}
			}

			if tc.wantWarn == "" && warnings.Len() != 0 || !strings.Contains(warnings.String(), tc.wantWarn) {
				t.Errorf("warnings = %q, want %q", warnings.String(), tc.wantWarn)
			}

			if succeeded := strings.Contains(out.String(), "holds all 2 planned files"); succeeded != (err == nil) {
				t.Errorf("out = %q with err %v", out.String(), err)
			}
		})
	}
}

// Without --asset or --assembly the planned files are the ones `release
// publish` uploads from the same dist: no second list names them.
func TestValidatePublishedRelease_PlansTheFilesPublishUploads(t *testing.T) {
	t.Chdir(t.TempDir())
	writeReleaseFilesFixture(t)

	paths, err := apprelease.PublishableReleaseFiles(apprelease.FilesInput{})
	if err != nil {
		t.Fatal(err)
	}

	var published []provider.ListedAsset

	for _, path := range paths {
		if name := filepath.Base(path); name != "app_checksums.txt.bundle" {
			published = append(published, provider.ListedAsset{Name: name, SizeUnknown: true})
		}
	}

	forge := holding(published...)

	err = apprelease.ValidatePublishedRelease(t.Context(), forge, &bytes.Buffer{}, output.Annotator{},
		apprelease.ValidatePublishedInput{Tag: "v1.0.0", Repository: "owner/app"})
	if !errors.Is(err, errs.ErrValidation) || !strings.HasSuffix(err.Error(), "planned: missing app_checksums.txt.bundle: "+errs.ErrValidation.Error()) {
		t.Fatalf("err = %v, want only the checksum bundle missing", err)
	}
}

// An assembly plans its assets and each signature found next to one, as
// `release publish --strategy=recreate` uploads them.
func TestValidatePublishedRelease_PlansAnAssemblyWithItsSignatures(t *testing.T) {
	t.Chdir(t.TempDir())
	mustWrite(t, "files/app.jar", "jar")
	mustWrite(t, "files/app.jar.asc", "signature")
	writeAssembly(t, "assembly.json", domainrelease.Assembly{
		Version: domainrelease.AssemblyVersion,
		Assets:  []domainrelease.AssemblyFile{{Path: "files/app.jar", Name: "app.jar", Required: true}},
	})

	forge := holding(provider.ListedAsset{Name: "app.jar", Size: 3})

	err := apprelease.ValidatePublishedRelease(t.Context(), forge, &bytes.Buffer{}, output.Annotator{},
		apprelease.ValidatePublishedInput{Tag: "v1.0.0", Repository: "owner/app", AssemblyFile: "assembly.json"})
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "planned: missing app.jar.asc: ") {
		t.Fatalf("err = %v, want only the signature missing", err)
	}
}

// An assembly that plans no file, as a meta or container-only project's
// does, is published by `release publish --strategy=recreate` as a release
// without files. Reading it back still proves the release is visible and
// not a draft, and a file on it is still a warning.
func TestValidatePublishedRelease_ReadsBackAnAssemblyThatPlansNoFile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		forge    forgeRelease
		wantErr  string // empty: the command succeeds
		wantWarn string
	}{
		{name: "a release without files", forge: holding()},
		{name: "a file the plan does not name warns", forge: holding(provider.ListedAsset{Name: "notes.pdf", Size: 1}), wantWarn: "holds notes.pdf"},
		{name: "no release for the tag", wantErr: "no release v1.0.0 is visible"},
		{name: "a release left a draft", forge: forgeRelease{release: &provider.ListedRelease{Tag: "v1.0.0", Draft: true}}, wantErr: "still a draft"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out, warnings bytes.Buffer

			err := apprelease.ValidatePublishedRelease(t.Context(), tc.forge, &out,
				output.NewAnnotator(&warnings, output.FormatText), apprelease.ValidatePublishedInput{
					Tag: "v1.0.0", Repository: "owner/app", AssemblyFile: emptyAssembly(t),
				})

			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v, want success", err)
			case tc.wantErr != "" && (!errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want a validation error naming %q", err, tc.wantErr)
			}

			if tc.wantWarn == "" && warnings.Len() != 0 || !strings.Contains(warnings.String(), tc.wantWarn) {
				t.Errorf("warnings = %q, want %q", warnings.String(), tc.wantWarn)
			}

			if succeeded := strings.Contains(out.String(), "holds no file, as planned"); succeeded != (err == nil) {
				t.Errorf("out = %q with err %v", out.String(), err)
			}
		})
	}
}

func TestValidatePublishedRelease_RefusesInputThatNamesNoPlan(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "app.tar.gz")
	mustWrite(t, file, "archive")

	cases := []struct {
		name string
		in   apprelease.ValidatePublishedInput
		want error
	}{
		{name: "no tag", in: apprelease.ValidatePublishedInput{Repository: "owner/app", Assets: []string{file}}, want: errs.ErrUsage},
		{name: "no repository", in: apprelease.ValidatePublishedInput{Tag: "v1.0.0", Assets: []string{file}}, want: errs.ErrUsage},
		{name: "assembly and assets", in: apprelease.ValidatePublishedInput{Tag: "v1.0.0", Repository: "owner/app", Assets: []string{file}, AssemblyFile: "a.json"}, want: errs.ErrUsage},
		{name: "a planned file that is absent", in: apprelease.ValidatePublishedInput{Tag: "v1.0.0", Repository: "owner/app", Assets: []string{file + ".absent"}}, want: errs.ErrMissingInput},
		{name: "a planned file that is a directory", in: apprelease.ValidatePublishedInput{Tag: "v1.0.0", Repository: "owner/app", Assets: []string{filepath.Dir(file)}}, want: errs.ErrMissingInput},
		{name: "two planned files with one name", in: apprelease.ValidatePublishedInput{Tag: "v1.0.0", Repository: "owner/app", Assets: []string{file, file}}, want: errs.ErrValidation},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := apprelease.ValidatePublishedRelease(t.Context(), holding(provider.ListedAsset{Name: "app.tar.gz", Size: 7}), &bytes.Buffer{}, output.Annotator{}, tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
