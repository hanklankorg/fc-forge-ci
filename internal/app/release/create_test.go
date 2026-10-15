// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeprovider"
)

// fakeFS is an in-memory fsOps. Files map filepath → exists; Empty set
// to true for files that exist but are empty.
type fakeFS struct {
	Files map[string]bool
	Empty map[string]bool
}

func (f *fakeFS) FileNonEmpty(p string) bool {
	return f.Files[p] && !f.Empty[p]
}

// emptyAssembly writes a release assembly that lists no assets and returns
// its path, for tests whose subject is the release itself rather than what is
// attached to it.
func emptyAssembly(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "release-assembly.json")
	writeAssembly(t, path, domainrelease.Assembly{Version: domainrelease.AssemblyVersion})

	return path
}

func TestCreateRelease_RequiresTagAndRepo(t *testing.T) {
	t.Parallel()
	prov := fakeprovider.New(t)

	fs := &fakeFS{Files: map[string]bool{}}
	if err := apprelease.CreateRelease(context.Background(), prov, fs, &bytes.Buffer{}, apprelease.CreateReleaseInput{
		Repository: "owner/repo",
	}); !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "tag is required") {
		t.Errorf("missing tag err = %v, want ErrUsage", err)
	}

	if err := apprelease.CreateRelease(context.Background(), prov, fs, &bytes.Buffer{}, apprelease.CreateReleaseInput{
		Tag: "v1.0.0",
	}); !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "REPOSITORY") {
		t.Errorf("missing repo err = %v, want ErrUsage", err)
	}
}

// TestCreateRelease_RequiresAssembly: the recreate strategy uploads exactly
// what a release assembly lists, so a call that names none is refused before
// the forge is asked anything.
func TestCreateRelease_RequiresAssembly(t *testing.T) {
	t.Parallel()
	prov := fakeprovider.New(t)

	err := apprelease.CreateRelease(context.Background(), prov, &fakeFS{}, &bytes.Buffer{}, apprelease.CreateReleaseInput{
		Tag:        "v1.0.0",
		Repository: "owner/repo",
	})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "--assembly") {
		t.Errorf("err = %v, want ErrUsage naming --assembly", err)
	}

	if calls := prov.CreateReleaseCalls(); len(calls) != 0 {
		t.Errorf("provider calls = %d, want none", len(calls))
	}
}

// TestCreateRelease_MarksOnlyPrereleaseTagsAsPrerelease states the rule in both
// directions. The negative case used to sit inside the full-release test, where
// a reader looking for the prerelease rule would not find it.
func TestCreateRelease_MarksOnlyPrereleaseTagsAsPrerelease(t *testing.T) {
	t.Parallel()

	for tag, want := range map[string]bool{
		"v1.0.0":           false,
		"v1.0.0-rc.1":      true,
		"v1.0.0-preview.1": true,
	} {
		t.Run(tag, func(t *testing.T) {
			t.Parallel()

			prov := fakeprovider.New(t)

			err := apprelease.CreateRelease(context.Background(), prov, &fakeFS{Files: map[string]bool{}}, &bytes.Buffer{}, apprelease.CreateReleaseInput{
				Tag:          tag,
				Repository:   "owner/repo",
				AssemblyFile: emptyAssembly(t),
			})
			if err != nil {
				t.Fatal(err)
			}

			if got := prov.CreateReleaseCalls()[0].Spec.Prerelease; got != want {
				t.Errorf("prerelease = %v, want %v", got, want)
			}
		})
	}
}

func TestCreateRelease_DraftFlagPropagates(t *testing.T) {
	t.Parallel()
	prov := fakeprovider.New(t)
	fs := &fakeFS{Files: map[string]bool{}}

	err := apprelease.CreateRelease(context.Background(), prov, fs, &bytes.Buffer{}, apprelease.CreateReleaseInput{
		Tag:          "v1.0.0",
		Repository:   "owner/repo",
		Draft:        true,
		MakeLatest:   "false",
		AssemblyFile: emptyAssembly(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	spec := prov.CreateReleaseCalls()[0].Spec
	if !spec.Draft {
		t.Error("Draft should be true")
	}

	if spec.MakeLatest != provider.MakeLatestFalse {
		t.Error("MakeLatest should be false")
	}
}

func TestCreateRelease_MakeLatestLegacyPropagates(t *testing.T) {
	t.Parallel()

	prov := fakeprovider.New(t)
	fs := &fakeFS{Files: map[string]bool{}}

	err := apprelease.CreateRelease(context.Background(), prov, fs, &bytes.Buffer{}, apprelease.CreateReleaseInput{
		Tag:          "v1.0.0",
		Repository:   "owner/repo",
		MakeLatest:   "legacy",
		AssemblyFile: emptyAssembly(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := prov.CreateReleaseCalls()[0].Spec.MakeLatest; got != provider.MakeLatestLegacy {
		t.Errorf("MakeLatest = %q, want %q", got, provider.MakeLatestLegacy)
	}
}

func TestCreateRelease_NotesFilePassesThroughWhenNonEmpty(t *testing.T) {
	t.Parallel()
	prov := fakeprovider.New(t)

	fs := &fakeFS{Files: map[string]bool{"notes.md": true}}
	if err := apprelease.CreateRelease(context.Background(), prov, fs, &bytes.Buffer{}, apprelease.CreateReleaseInput{
		Tag:              "v1.0.0",
		Repository:       "owner/repo",
		ReleaseNotesFile: "notes.md",
		AssemblyFile:     emptyAssembly(t),
	}); err != nil {
		t.Fatal(err)
	}

	if got := prov.CreateReleaseCalls()[0].Spec.NotesFile; got != "notes.md" {
		t.Errorf("NotesFile = %q", got)
	}
}

func TestCreateRelease_SkipsMissingOrEmptyNotesFile(t *testing.T) {
	t.Parallel()

	tests := map[string]*fakeFS{
		"missing": {Files: map[string]bool{}},
		"empty": {
			Files: map[string]bool{"notes.md": true},
			Empty: map[string]bool{"notes.md": true},
		},
	}
	for name, fs := range tests {
		t.Run(name, func(t *testing.T) {
			prov := fakeprovider.New(t)
			if err := apprelease.CreateRelease(context.Background(), prov, fs, &bytes.Buffer{}, apprelease.CreateReleaseInput{
				Tag:              "v1.0.0",
				Repository:       "owner/repo",
				ReleaseNotesFile: "notes.md",
				AssemblyFile:     emptyAssembly(t),
			}); err != nil {
				t.Fatal(err)
			}

			if got := prov.CreateReleaseCalls()[0].Spec.NotesFile; got != "" {
				t.Errorf("NotesFile = %q, want empty", got)
			}
		})
	}
}

func TestCreateRelease_PropagatesProviderError(t *testing.T) {
	t.Parallel()
	prov := fakeprovider.New(t).WithCreateReleaseError(errForgeRefused)
	fs := &fakeFS{Files: map[string]bool{}}

	err := apprelease.CreateRelease(context.Background(), prov, fs, &bytes.Buffer{}, apprelease.CreateReleaseInput{
		Tag:          "v1.0.0",
		Repository:   "owner/repo",
		AssemblyFile: emptyAssembly(t),
	})
	if !errors.Is(err, errForgeRefused) {
		t.Errorf("err = %v, want the forge's own error to survive wrapping", err)
	}
}

type fakeError string

func (e fakeError) Error() string { return string(e) }

// Named so the propagation test asserts identity rather than a substring
// of whatever message the fake happens to carry.
const errForgeRefused = fakeError("forge refused the release")
