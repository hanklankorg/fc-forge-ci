// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func TestGoReleaserBuildIsUnsignedAndPreservesArgumentBoundaries(t *testing.T) {
	t.Parallel()

	action := loadAction(t, "build-goreleaser")
	action.requireSteps(t, 2)
	action.requireStepEnv(t, 0, map[string]string{
		"RELEASE_SHA": "${{ inputs.release-sha }}", "GORELEASER_CURRENT_TAG": "${{ inputs.release-tag }}",
		"GORELEASER_CONFIG": "${{ inputs.config }}", "RELEASE_NOTES": "${{ inputs.release-notes }}",
		"GITHUB_TOKEN": "", "GH_TOKEN": "",
	})

	for _, test := range []struct {
		name, checkedOut, expected string
		buildStatus, want          int
	}{
		{"build", "prepared-sha", "prepared-sha", 0, 0},
		{"wrong source", "other-sha", "prepared-sha", 0, 1},
		{"builder failed", "prepared-sha", "prepared-sha", 37, 37},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "build-goreleaser")
			box.write("tools/git", "#!/bin/sh\nset -eu\ntest \"$*\" = 'rev-parse HEAD'\nprintf '%s\\n' \"$CHECKED_OUT\"\n", 0o700)
			box.write("tools/goreleaser", `#!/bin/sh
set -eu
test "$GORELEASER_CURRENT_TAG" = v1.2.3
test -z "${GITHUB_TOKEN-}"
test -z "${GH_TOKEN-}"
printf '%s\0' "$@" >"$FAKE_ARGV_LOG"
exit "$BUILD_STATUS"
`, 0o700)

			got := box.run(action.Runs.Steps[0].Run,
				"PATH="+filepath.Join(box.root, "tools")+":"+os.Getenv("PATH"),
				"CHECKED_OUT="+test.checkedOut, "RELEASE_SHA="+test.expected,
				"GORELEASER_CURRENT_TAG=v1.2.3", "GORELEASER_CONFIG=config with spaces.yml",
				"RELEASE_NOTES=notes with spaces.md", "BUILD_STATUS="+strconv.Itoa(test.buildStatus))
			if got.status != test.want {
				t.Fatalf("status %d, expected %d: %s", got.status, test.want, got.stderr)
			}

			if test.checkedOut != test.expected {
				if len(box.argv()) != 0 {
					t.Fatal("wrong source reached the builder")
				}

				return
			}

			want := []string{"release", "--clean", "--skip=publish,sign,announce", "--config", "config with spaces.yml", "--release-notes", "notes with spaces.md"}
			if !slices.Equal(box.argv(), want) {
				t.Fatalf("builder argv %q, expected %q", box.argv(), want)
			}
		})
	}
}

func TestGoReleaserBuildImportsThroughThePinnedEngine(t *testing.T) {
	t.Parallel()

	action := loadAction(t, "build-goreleaser")
	box := newSandbox(t, "build-goreleaser")
	box.write("notes.md", "public release notes", 0o600)
	box.write("dist tree/.keep", "", 0o600)

	got := box.run(action.Runs.Steps[1].Run, "RELEASE_NOTES=notes.md", "DIST_DIRECTORY=dist tree", "FAKE_ENGINE_STATUS=17")
	if got.status != 17 {
		t.Fatalf("engine status lost: %d: %s", got.status, got.stderr)
	}

	want := []string{"release", "files", "import-goreleaser", "--dist-dir", "dist tree", "--manifest", "dist tree/release-files.json"}
	if !slices.Equal(box.argv(), want) {
		t.Fatalf("manifest import argv %q, expected %q", box.argv(), want)
	}

	notes, err := os.ReadFile(filepath.Join(box.root, "dist tree/release-notes.md"))
	if err != nil || string(notes) != "public release notes" {
		t.Fatalf("release notes not staged: %v", err)
	}
}
