// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package buildah_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

// A Containerfile's own ignore file is named to buildah explicitly, so every
// buildah version builds the context its author wrote. Buildah 1.39 reads only
// the context's .containerignore, and a release image whose own allowlist
// admitted its prebuilt binary failed at COPY under the root one.
func TestBuild_NamesTheContainerfilesOwnIgnoreFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	containerfile := filepath.Join(dir, "packaging", "Containerfile.release")
	write := func(path string) {
		t.Helper()

		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte("*\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(containerfile)

	build := func(t *testing.T, file string) string {
		t.Helper()

		m := mockbinary.New(t)
		m.Add("buildah", `printf 'ARGV %s\n' "$*" >&2`)

		var stderr strings.Builder

		err := (&buildah.Adapter{Bin: m.Path("buildah")}).BuildSignerImage(context.Background(), container.SignerImageBuildToolRequest{
			Platform: "linux/amd64", SourceURL: "https://example.com/org/repo", Revision: "abc123",
			LocalImage: "localhost/image:amd64", Containerfile: file, Context: dir,
		}, &stderr)
		if err != nil {
			t.Fatal(err)
		}

		return stderr.String()
	}

	if argv := build(t, containerfile); strings.Contains(argv, "--ignorefile") {
		t.Fatalf("no own ignore file, yet buildah was given one: %s", argv)
	}

	write(containerfile + ".dockerignore")

	if argv := build(t, containerfile); !strings.Contains(argv, "--ignorefile "+containerfile+".dockerignore ") {
		t.Fatalf("own .dockerignore not named: %s", argv)
	}

	write(containerfile + ".containerignore")

	argv := build(t, containerfile)
	if !strings.Contains(argv, "--ignorefile "+containerfile+".containerignore ") {
		t.Fatalf(".containerignore must win over .dockerignore: %s", argv)
	}

	if strings.Index(argv, "--ignorefile") > strings.LastIndex(argv, dir) {
		t.Fatalf("the context must stay the last argument: %s", argv)
	}
}
