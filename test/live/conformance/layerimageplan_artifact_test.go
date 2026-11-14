// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/zip"
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

func TestImagePlanArtifactTree(t *testing.T) {
	want := map[string][]byte{"ledger.json": []byte("{}\n"), "nested/marker": {0, 255, '\n'}}
	archive := imagePlanArtifactZIP(t, want)
	empty := imagePlanArtifactZIP(t, map[string][]byte{})

	for _, name := range []string{"resolved-base-review-cold", "resolved-final-review-resolved-base-review-cold"} {
		t.Run(name, func(t *testing.T) {
			selected := livetest.RunArtifact{ID: 41, Name: name}
			duplicate := livetest.RunArtifact{ID: 42, Name: name}
			expired := selected
			expired.Expired = true
			unrelated := livetest.RunArtifact{ID: 99, Name: name + "-other"}

			for _, tc := range []struct {
				name      string
				artifacts []livetest.RunArtifact
				archive   []byte
				limit     int64
				want      map[string][]byte
				err       string
				downloads []int64
			}{
				{name: "missing", artifacts: []livetest.RunArtifact{unrelated}, limit: 6},
				{name: "nil artifact list", limit: 6},
				{name: "exact bytes at bound", artifacts: []livetest.RunArtifact{unrelated, selected, unrelated}, archive: archive, limit: 6, want: want, downloads: []int64{41}},
				{name: "single empty tree", artifacts: []livetest.RunArtifact{selected}, archive: empty, limit: 6, want: map[string][]byte{}, downloads: []int64{41}},
				{name: "expired metadata unchanged", artifacts: []livetest.RunArtifact{expired}, archive: archive, limit: 6, want: want, downloads: []int64{41}},
				{name: "duplicate", artifacts: []livetest.RunArtifact{selected, duplicate}, archive: archive, limit: 6, err: "duplicate review artifact", downloads: []int64{41}},
				{name: "empty tree duplicate", artifacts: []livetest.RunArtifact{selected, duplicate}, archive: empty, limit: 6, err: "duplicate review artifact", downloads: []int64{41}},
				{name: "malformed", artifacts: []livetest.RunArtifact{selected}, archive: []byte("not a ZIP"), limit: 6, err: "read the archive", downloads: []int64{41}},
				{name: "malformed before duplicate", artifacts: []livetest.RunArtifact{selected, duplicate}, archive: []byte("not a ZIP"), limit: 6, err: "read the archive", downloads: []int64{41}},
				{name: "oversized total", artifacts: []livetest.RunArtifact{selected}, archive: archive, limit: 5, err: "size limit", downloads: []int64{41}},
				{name: "oversized entry", artifacts: []livetest.RunArtifact{selected}, archive: archive, limit: 2, err: "size limit", downloads: []int64{41}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var downloads []int64

					tree, err := imagePlanArtifactTree(tc.artifacts, name, tc.limit, func(id int64) []byte {
						downloads = append(downloads, id)

						return tc.archive
					})
					if (err == nil) != (tc.err == "") || (err != nil && !strings.Contains(err.Error(), tc.err)) {
						t.Fatalf("error = %v, want %q", err, tc.err)
					}

					if !reflect.DeepEqual(tree, tc.want) || !reflect.DeepEqual(downloads, tc.downloads) {
						t.Fatalf("tree = %v, downloads = %v; want %v, %v", tree, downloads, tc.want, tc.downloads)
					}
				})
			}
		})
	}
}

func imagePlanArtifactZIP(t *testing.T, tree map[string][]byte) []byte {
	t.Helper()

	var archive bytes.Buffer

	writer := zip.NewWriter(&archive)

	for name, content := range tree {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return archive.Bytes()
}
