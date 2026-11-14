// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// fakeMise lays out a mise install with a cosign shim: shims/cosign is a
// symlink to a fake mise whose `which` prints which, or fails when it is empty.
func fakeMise(t *testing.T, which string) (string, string) {
	t.Helper()

	root := testfs.NewReal(t).Root
	bin := filepath.Join(root, "bin")
	shims := filepath.Join(root, "data", "mise", "shims")
	tool := filepath.Join(root, "installs", "cosign", "cosign")

	for _, dir := range []string{bin, shims, filepath.Dir(tool)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	script := "#!/bin/sh\necho 'mise ERROR cosign is not installed' >&2\nexit 1\n"
	if which != "" {
		script = "#!/bin/sh\n[ \"$1 $2\" = 'which cosign' ] || exit 3\necho '" + which + "'\n"
	}

	if err := os.WriteFile(filepath.Join(bin, "mise"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(filepath.Join(bin, "mise"), filepath.Join(shims, "cosign")); err != nil {
		t.Fatal(err)
	}

	return shims, tool
}

func TestResolveHostTool_ResolvesAMiseShimToTheInstalledTool(t *testing.T) {
	emptyShims, tool := fakeMise(t, "")
	installedShims, _ := fakeMise(t, tool)

	t.Run("installed", func(t *testing.T) {
		t.Setenv("PATH", installedShims)

		got, err := resolveHostTool(t.Context(), "cosign", t.TempDir())
		if err != nil || got != tool {
			t.Fatalf("resolveHostTool = %q, %v; want %q", got, err, tool)
		}
	})

	t.Run("nothing installed behind the shim", func(t *testing.T) {
		t.Setenv("PATH", emptyShims)

		_, err := resolveHostTool(t.Context(), "cosign", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "mise install && mise reshim") || !strings.Contains(err.Error(), "not installed") {
			t.Fatalf("resolveHostTool error = %v; want a mise install && mise reshim refusal", err)
		}
	})

	t.Run("a tool that is not a shim", func(t *testing.T) {
		t.Setenv("PATH", filepath.Dir(tool))

		got, err := resolveHostTool(t.Context(), "cosign", t.TempDir())
		if err != nil || got != tool {
			t.Fatalf("resolveHostTool = %q, %v; want %q", got, err, tool)
		}
	})

	t.Run("missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())

		if _, err := resolveHostTool(t.Context(), "cosign", t.TempDir()); err == nil || !strings.Contains(err.Error(), "not on PATH") {
			t.Fatalf("resolveHostTool error = %v; want not on PATH", err)
		}
	})
}

func TestResolveHostTool_NeverTakesMiseForTheTool(t *testing.T) {
	t.Run("mise which answers with mise", func(t *testing.T) {
		// Another mise, as a stale pin or a shim of a shim would name.
		other := filepath.Join(t.TempDir(), "mise")
		if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}

		shims, _ := fakeMise(t, other)
		t.Setenv("PATH", shims)

		_, err := resolveHostTool(t.Context(), "cosign", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "the mise binary rather than the tool") {
			t.Fatalf("resolveHostTool error = %v; want a refusal to take mise for cosign", err)
		}
	})

	t.Run("a shim that is not a link to mise", func(t *testing.T) {
		root := t.TempDir()
		shims := filepath.Join(root, "mise", "shims")
		onPath := filepath.Join(root, "bin")
		marker := filepath.Join(root, "mise-ran")

		for _, dir := range []string{shims, onPath} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}

		if err := os.WriteFile(filepath.Join(shims, "cosign"), []byte("#!/bin/sh\nexec mise x -- cosign \"$@\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}

		// A mise on PATH must not be the one asked: nothing ties it to the shims.
		if err := os.WriteFile(filepath.Join(onPath, "mise"), []byte("#!/bin/sh\n: >'"+marker+"'\necho /bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}

		t.Setenv("PATH", shims+string(os.PathListSeparator)+onPath)

		_, err := resolveHostTool(t.Context(), "cosign", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "not a link to mise") || !strings.Contains(err.Error(), "mise reshim") {
			t.Fatalf("resolveHostTool error = %v; want a refusal naming mise reshim", err)
		}

		if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
			t.Fatal("resolveHostTool ran the mise found on PATH")
		}
	})
}

func TestCheckHostTools_ListsEveryToolThatDoesNotResolve(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := CheckHostTools(t.Context(), []string{"cosign", "syft"})
	if err == nil || !strings.Contains(err.Error(), "cosign") || !strings.Contains(err.Error(), "syft") {
		t.Fatalf("CheckHostTools error = %v; want both tools named", err)
	}
}
