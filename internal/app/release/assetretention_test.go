// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// releasePruner is a forge holding releases; it records each deletion.
type releasePruner struct {
	releases []provider.ListedRelease
	deleted  []string
}

func (p *releasePruner) ListReleases(context.Context, string) ([]provider.ListedRelease, error) {
	return p.releases, nil
}

func (p *releasePruner) DeleteReleaseAsset(_ context.Context, _ string, release provider.ListedRelease, asset provider.ListedAsset) error {
	p.deleted = append(p.deleted, release.Tag+"/"+asset.Name)

	return nil
}

// goRelease is a release as a Go project publishes it: archives, a package,
// the checksum manifest, bundles and an SBOM.
func goRelease(tag string, day int) provider.ListedRelease {
	return provider.ListedRelease{
		Tag:     tag,
		Created: time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC),
		Assets: []provider.ListedAsset{
			{Name: "app_" + tag + "_linux_amd64.tar.gz", Size: 10e6},
			{Name: "app_" + tag + "_amd64.deb", Size: 5e6},
			{Name: "app_" + tag + "_checksums.txt", Size: 1e3},
			{Name: "app_" + tag + "_checksums.txt.bundle", Size: 4e3},
			{Name: "app_" + tag + "_linux_amd64.tar.gz.bundle", Size: 4e3},
			{Name: "app_" + tag + "_amd64.deb.sig", Size: 1e3},
			{Name: "app-" + tag + "-abc1234-build-sbom.cyclonedx.json", Size: 2e5},
			{Name: "SHA256SUMS", Size: 1e3},
		},
	}
}

func retention(keep int) apprelease.AssetRetentionInput {
	return apprelease.AssetRetentionInput{
		Repository: "owner/app",
		Keep:       keep,
		KeepFiles:  regexp.MustCompile(apprelease.DefaultAssetRetentionKeepFiles),
	}
}

// The newest releases keep everything, whatever order the forge lists them
// in; an older one keeps its checksums, signatures and SBOM and loses only
// its archives and packages. Drafts are neither counted nor touched.
func TestApplyAssetRetention_TrimsOnlyOldReleasesToTheirEvidence(t *testing.T) {
	t.Parallel()

	draft := goRelease("v0.9.0", 1)
	draft.Draft = true
	pruner := &releasePruner{releases: []provider.ListedRelease{
		goRelease("v1.1.0", 3), goRelease("v1.0.0", 2), goRelease("v1.2.0", 4), draft,
	}}

	var out bytes.Buffer

	result, err := apprelease.ApplyAssetRetention(t.Context(), pruner, &out, retention(2))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"v1.0.0/app_v1.0.0_linux_amd64.tar.gz", "v1.0.0/app_v1.0.0_amd64.deb"}
	if !slices.Equal(pruner.deleted, want) || result.Files != 2 || result.Bytes != 15e6 {
		t.Errorf("deleted %v, result %+v; want %v and 15 MB\n%s", pruner.deleted, result, want, out.String())
	}

	if !strings.Contains(out.String(), "keep          v1.2.0") || !strings.Contains(out.String(), "trim          v1.0.0 (keeps 6 of 8 files)") {
		t.Errorf("the plan does not say what is kept:\n%s", out.String())
	}
}

func TestApplyAssetRetention_DryRunDeletesNothing(t *testing.T) {
	t.Parallel()

	pruner := &releasePruner{releases: []provider.ListedRelease{goRelease("v1.0.0", 1), goRelease("v1.1.0", 2)}}
	in := retention(1)
	in.DryRun = true

	var out bytes.Buffer

	result, err := apprelease.ApplyAssetRetention(t.Context(), pruner, &out, in)
	if err != nil || len(pruner.deleted) != 0 || result.Files != 2 || !strings.Contains(out.String(), "would delete") {
		t.Errorf("dry run deleted %v, result %+v, %v:\n%s", pruner.deleted, result, err, out.String())
	}
}

// A pass larger than the bound deletes nothing, so a first run over years of
// releases is read before it acts.
func TestApplyAssetRetention_RefusesMoreThanTheBound(t *testing.T) {
	t.Parallel()

	pruner := &releasePruner{releases: []provider.ListedRelease{goRelease("v1.0.0", 1), goRelease("v1.1.0", 2), goRelease("v1.2.0", 3)}}
	in := retention(1)
	in.MaxDelete = 3

	if _, err := apprelease.ApplyAssetRetention(t.Context(), pruner, &bytes.Buffer{}, in); !errors.Is(err, errs.ErrValidation) || len(pruner.deleted) != 0 {
		t.Errorf("a pass of 4 files under a bound of 3 = %v, deleted %v; want a refusal before any deletion", err, pruner.deleted)
	}
}

// An immutable release counts among the releases, keeps its files and says
// why; the pass goes on to the older ones.
func TestApplyAssetRetention_KeepsAnImmutableReleaseWhole(t *testing.T) {
	t.Parallel()

	immutable := goRelease("v1.1.0", 2)
	immutable.Immutable = true
	pruner := &releasePruner{releases: []provider.ListedRelease{goRelease("v1.0.0", 1), immutable, goRelease("v1.2.0", 3)}}

	var out bytes.Buffer

	result, err := apprelease.ApplyAssetRetention(t.Context(), pruner, &out, retention(1))
	if err != nil || result.Files != 2 || len(pruner.deleted) != 2 || !strings.Contains(out.String(), "keep          v1.1.0 (immutable") {
		t.Errorf("result %+v, deleted %v, %v:\n%s", result, pruner.deleted, err, out.String())
	}
}

func TestApplyAssetRetention_RefusesWhatItCannotMean(t *testing.T) {
	t.Parallel()

	for name, in := range map[string]apprelease.AssetRetentionInput{
		"no repository":   {Keep: 6, KeepFiles: regexp.MustCompile(".")},
		"keep nothing":    {Repository: "owner/app", KeepFiles: regexp.MustCompile(".")},
		"no keep pattern": {Repository: "owner/app", Keep: 6},
		"negative bound":  {Repository: "owner/app", Keep: 6, KeepFiles: regexp.MustCompile("."), MaxDelete: -1},
	} {
		if _, err := apprelease.ApplyAssetRetention(t.Context(), &releasePruner{}, &bytes.Buffer{}, in); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("%s: %v; want a usage refusal", name, err)
		}
	}
}
