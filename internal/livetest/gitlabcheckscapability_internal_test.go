// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitLabChecksCapabilityUsesOnlyAdmittedGoFixtureMarker(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, marker, kind string
		link, want, refuse bool
	}{
		{name: "ordinary consumer"},
		{name: "Go alone is not capability", kind: "go\n"},
		{name: "Go integration", marker: "buildah-integration\n", kind: "go\n", want: true},
		{name: "Cargo cannot opt in", marker: "buildah-integration\n", kind: "cargo\n", refuse: true},
		{name: "unknown capability", marker: "privileged\n", kind: "go\n", refuse: true},
		{name: "linked marker", marker: "buildah-integration\n", kind: "go\n", link: true, refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			git, root := newConsumerCheckout(t, consumerSHA1)
			if tc.kind != "" {
				writeLayerFile(t, root, ".gitlab/generic-cli-kind", tc.kind)
			}
			if tc.marker != "" {
				writeLayerFile(t, root, ".gitlab/marker", tc.marker)
				if tc.link {
					if err := os.Symlink("marker", filepath.Join(root, ".gitlab/generic-cli-checks")); err != nil {
						t.Fatal(err)
					}
				} else {
					writeLayerFile(t, root, ".gitlab/generic-cli-checks", tc.marker)
				}
			}
			mustLayerGit(t, git, root, "add", "--all")
			mustLayerGit(t, git, root, "commit", "--quiet", "--allow-empty", "--message", "fixture capability")
			consumer, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// Mutable host files do not grant or remove capability after admission.
			writeLayerFile(t, root, ".gitlab/generic-cli-checks", "hostile\n")
			got, err := consumer.gitlabChecksNeedBuildah(t.Context())
			if got != tc.want || (err != nil) != tc.refuse {
				t.Fatalf("capability=%t err=%v", got, err)
			}
		})
	}
}
