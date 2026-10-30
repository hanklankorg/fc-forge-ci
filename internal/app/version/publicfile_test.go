// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

func TestPublicVersionFileRestrictiveUmask(t *testing.T) {
	evidence.UnderUmask(t, "^TestPublicVersionFileModes$")
}

func TestPublicVersionFileModes(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, existing := range []bool{false, true} {
		if existing {
			if err := root.Chmod(defaultChangelogFile, 0o640); err != nil {
				t.Fatal(err)
			}
		}
		if err := writePlanChangelog(root, []byte("updated\n")); err != nil {
			t.Fatal(err)
		}
		info, err := root.Stat(defaultChangelogFile)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o644)
		if existing {
			want = 0o640
		}
		if info.Mode().Perm() != want {
			t.Fatalf("existing=%v: mode=%o, want %o", existing, info.Mode().Perm(), want)
		}
	}
	if err := bumpXcodeIOS(root, "release.xcconfig", "release.xcconfig", "1.2.3", os.Stderr); err != nil {
		t.Fatal(err)
	}
	info, err := root.Stat("release.xcconfig")
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("new Xcode mode: %v, %v", info, err)
	}
	if err := root.Chmod("release.xcconfig", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bumpXcodeIOS(root, "release.xcconfig", "release.xcconfig", "1.2.4", os.Stderr); err != nil {
		t.Fatal(err)
	}
	info, err = root.Stat("release.xcconfig")
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("existing Xcode mode: %v, %v", info, err)
	}
}
