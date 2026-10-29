// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommandExistsNetworkingHelpers(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, location := range []string{"usr/lib/podman", "usr/libexec/podman", "path"} {
		t.Run(location, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), location)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if location == "path" {
				t.Setenv("PATH", dir)
			}
			for _, name := range []string{"netavark", "aardvark-dns"} {
				path := filepath.Join(dir, name)
				if commandExists(name, []string{dir}) {
					t.Fatalf("missing %s recognized", name)
				}
				if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if commandExists(name, []string{dir}) {
					t.Fatalf("non-executable %s recognized", name)
				}
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if !commandExists(name, []string{dir}) {
					t.Fatalf("installed %s not recognized", name)
				}
			}
		})
	}
}
