// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

type fakePackageStore struct {
	versions []domaincontainer.PackageVersion
	deleted  []int64
}

func (f *fakePackageStore) PackageVersions(context.Context, string, string) ([]domaincontainer.PackageVersion, error) {
	return f.versions, nil
}

func (f *fakePackageStore) DeletePackageVersion(_ context.Context, _, _ string, id int64) error {
	f.deleted = append(f.deleted, id)

	return nil
}

// fakeManifests serves manifests by digest; an unlisted digest is an image
// manifest, and failing digests return an error.
type fakeManifests struct {
	indexes map[string][]string
	failing map[string]bool
	read    []string
}

func (f *fakeManifests) Manifest(_ context.Context, ref string) ([]byte, error) {
	f.read = append(f.read, ref)

	_, digest, _ := strings.Cut(ref, "@")
	if f.failing[digest] {
		return nil, fmt.Errorf("read %s: %w", ref, errs.ErrDependencyUnavailable)
	}

	entries := make([]string, 0, len(f.indexes[digest]))
	for _, child := range f.indexes[digest] {
		entries = append(entries, `{"digest":"`+child+`"}`)
	}

	return []byte(`{"schemaVersion":2,"manifests":[` + strings.Join(entries, ",") + `]}`), nil
}

func retentionDigest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func retentionRule() domaincontainer.RetentionRule {
	return domaincontainer.RetentionRule{
		Keep:        regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`),
		Recent:      regexp.MustCompile(`^sha-`),
		RecentCount: 1,
	}
}

func retentionVersions() []domaincontainer.PackageVersion {
	day := func(n int) time.Time { return time.Date(2026, 10, n, 0, 0, 0, 0, time.UTC) }

	return []domaincontainer.PackageVersion{
		{ID: 1, Digest: retentionDigest('a'), Tags: []string{"v1.0.0"}, Created: day(1)},
		{ID: 2, Digest: retentionDigest('b'), Created: day(1)}, // the release's platform manifest
		{ID: 3, Digest: retentionDigest('c'), Tags: []string{"sha-old"}, Created: day(2)},
		{ID: 4, Digest: retentionDigest('d'), Created: day(2)}, // the old commit's platform manifest
		{ID: 5, Digest: retentionDigest('e'), Tags: []string{"sha-new"}, Created: day(3)},
	}
}

func TestApplyPackageRetention_DeletesWhatNoKeptImageNeeds(t *testing.T) {
	t.Parallel()

	store := &fakePackageStore{versions: retentionVersions()}
	manifests := &fakeManifests{indexes: map[string][]string{
		retentionDigest('a'): {retentionDigest('b')},
		retentionDigest('c'): {retentionDigest('d')},
	}}

	var out bytes.Buffer

	result, err := appcontainer.ApplyPackageRetention(t.Context(), store, manifests, &out, appcontainer.PackageRetentionInput{
		Repository: "ghcr.io/acme/runtime", Rule: retentionRule(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(store.deleted, []int64{3, 4}) || result.Kept != 3 {
		t.Fatalf("deleted %v, kept %d; want the old commit and its manifest deleted", store.deleted, result.Kept)
	}

	// Only kept manifests are read: the old commit's index is never opened.
	if slices.Contains(manifests.read, "ghcr.io/acme/runtime@"+retentionDigest('c')) {
		t.Errorf("read a manifest about to be deleted: %v", manifests.read)
	}
}

func TestApplyPackageRetention_DryRunDeletesNothing(t *testing.T) {
	t.Parallel()

	store := &fakePackageStore{versions: retentionVersions()}

	var out bytes.Buffer

	if _, err := appcontainer.ApplyPackageRetention(t.Context(), store, &fakeManifests{}, &out, appcontainer.PackageRetentionInput{
		Repository: "ghcr.io/acme/runtime", Rule: retentionRule(), DryRun: true,
	}); err != nil {
		t.Fatal(err)
	}

	if len(store.deleted) != 0 || !strings.Contains(out.String(), "would delete") {
		t.Fatalf("deleted %v; output:\n%s", store.deleted, out.String())
	}
}

func TestApplyPackageRetention_Refusals(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		in        appcontainer.PackageRetentionInput
		manifests *fakeManifests
		want      error
	}{
		"over the delete bound": {
			in:        appcontainer.PackageRetentionInput{Repository: "ghcr.io/acme/runtime", Rule: retentionRule(), MaxDelete: 1},
			manifests: &fakeManifests{},
			want:      errs.ErrValidation,
		},
		"a rule that keeps none of the versions": {
			in: appcontainer.PackageRetentionInput{Repository: "ghcr.io/acme/runtime", Rule: domaincontainer.RetentionRule{
				Keep: regexp.MustCompile(`^nothing$`),
			}},
			manifests: &fakeManifests{},
			want:      errs.ErrValidation,
		},
		"an unreadable kept manifest": {
			in:        appcontainer.PackageRetentionInput{Repository: "ghcr.io/acme/runtime", Rule: retentionRule()},
			manifests: &fakeManifests{failing: map[string]bool{retentionDigest('a'): true}},
			want:      errs.ErrDependencyUnavailable,
		},
		"not host/owner/name": {
			in:        appcontainer.PackageRetentionInput{Repository: "ghcr.io/acme", Rule: retentionRule()},
			manifests: &fakeManifests{},
			want:      errs.ErrUsage,
		},
	} {
		store := &fakePackageStore{versions: retentionVersions()}

		_, err := appcontainer.ApplyPackageRetention(t.Context(), store, tc.manifests, io.Discard, tc.in)
		if !errors.Is(err, tc.want) || len(store.deleted) != 0 {
			t.Errorf("%s: err = %v, deleted %v; want %v and nothing deleted", name, err, store.deleted, tc.want)
		}
	}
}
