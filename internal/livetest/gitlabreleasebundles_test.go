// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
)

func TestGitLabReleaseEvidenceHandoffDigests(t *testing.T) {
	for _, changed := range []string{"", "build", "publisher", "signer"} {
		t.Run("changed="+changed, func(t *testing.T) {
			dirs := make([]string, 0, 3)

			for _, stage := range []string{"build", "publisher", "signer"} {
				root := filepath.Join(t.TempDir(), stage)
				if err := os.MkdirAll(filepath.Join(root, "dist"), 0o700); err != nil {
					t.Fatal(err)
				}

				file := filepath.Join(root, "dist/subject")
				if err := os.WriteFile(file, []byte("original subject"), 0o600); err != nil {
					t.Fatal(err)
				}

				digest, err := apprelease.DistDigestWithManifestRoot(filepath.Join(root, "dist"), ".")
				if err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(filepath.Join(root, "digest"), []byte(digest+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}

				if stage == changed {
					if err := os.WriteFile(file, []byte("changed subject"), 0o600); err != nil {
						t.Fatal(err)
					}
				}

				dirs = append(dirs, root)
			}

			if err := verifyReleaseHandoffDigests(dirs...); (err != nil) != (changed != "") {
				t.Fatalf("handoff digest validation for %q: %v", changed, err)
			}

			if changed == "" {
				if err := verifyReleaseHandoffDigests(dirs[0], "", dirs[2]); err != nil {
					t.Fatalf("file-only chain: %v", err)
				}
			}
		})
	}
}
