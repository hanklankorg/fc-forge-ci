// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"strings"
	"testing"
)

// Images built from one base share most of their layers. A layout handed
// from a build job to its publish job as one whole artifact uploads those
// layers again for every image, and Forgejo counts each copy against the
// owner's storage quota until it refuses an upload with HTTP 413. Every
// layout handoff therefore goes through the namespace's blob store, on both
// sides with the same prefix: an upload without it spends the quota, and a
// download without it would hand publication a layout missing its blobs.
func TestLayoutHandoffsShareBlobsThroughTheRunsBlobStore(t *testing.T) {
	t.Parallel()

	for file, namespace := range map[string]string{
		".forgejo/workflows/release-images.yml":  "IMAGE_ARTIFACT_NAMESPACE",
		".forgejo/workflows/snapshot-images.yml": "SNAPSHOT_NAMESPACE",
	} {
		blobStore := `--blob-store "${` + namespace + `}-blob"`
		parsed := loadWorkflow(t, file)

		var uploads, downloads int

		for name, job := range parsed.jobs {
			for _, step := range job.steps() {
				run := scalarString(mappingValue(step, "run"))
				if !strings.Contains(run, "-layout-") {
					continue
				}

				for _, verb := range []string{"artifact upload", "artifact download"} {
					if !strings.Contains(run, verb) {
						continue
					}

					if !strings.Contains(run, blobStore) {
						t.Errorf("%s job %s: layout %s must pass %s", file, name, verb, blobStore)
					}

					if verb == "artifact upload" {
						uploads++
					} else {
						downloads++
					}
				}
			}
		}

		if uploads == 0 || downloads == 0 {
			t.Errorf("%s: found %d layout uploads and %d downloads; the handoff moved", file, uploads, downloads)
		}
	}
}
