// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package github_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestNumberedPrereleaseRetriesNeverReplacePublishedAssets(t *testing.T) {
	for _, method := range []string{"create", "reconcile"} {
		t.Run(method, func(t *testing.T) {
			fake := newFake()
			release := fake.preloadRelease(false, true)
			delete(fake.releases, release.TagName)
			release.TagName, release.Name = "v1.0.0-beta.2", "Beta"
			release.Assets = []*recordedAsset{{ID: 2001, Name: "app.tgz", Body: "artifact"}}
			fake.releases[release.TagName] = release

			srv := httptest.NewServer(fake.handler())
			defer srv.Close()

			path := filepath.Join(t.TempDir(), "app.tgz")
			if err := os.WriteFile(path, []byte("artifact"), 0o600); err != nil {
				t.Fatal(err)
			}

			spec := provider.ReleaseSpec{Immutable: true, Tag: release.TagName, Name: "Beta", Prerelease: true, MakeLatest: provider.MakeLatestFalse, Assets: []string{path}}

			publish := providerForFake(srv).CreateRelease
			if method == "reconcile" {
				publish = providerForFake(srv).PublishRelease
			}

			if err := publish(t.Context(), "owner/repo", spec); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := publish(t.Context(), "owner/repo", spec); !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("changed prerelease accepted: %v", err)
			}

			for _, call := range fake.Calls() {
				if call.Method != http.MethodGet {
					t.Fatalf("published beta mutated: %+v", call)
				}
			}
		})
	}
}
