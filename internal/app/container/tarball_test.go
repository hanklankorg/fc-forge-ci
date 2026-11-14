// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func makeTarball(t *testing.T, path string, files map[string]string) {
	t.Helper()

	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	for rel, body := range files {
		full := "package/" + rel
		if err := tw.WriteHeader(&tar.Header{
			Name: full, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// A project's build writes where its tool says: dist/, build/, Nuxt's hidden
// .output/. The package is unpacked as packed, under no directory name the
// engine knows.
func TestExtractNPMTarball_UnpacksWhateverDirectoryTheBuildWrote(t *testing.T) {
	for _, built := range []string{"dist/cli.js", "build/index.js", ".output/server/index.mjs", "lib/esm/main.js"} {
		fsys := testfs.NewReal(t)
		makeTarball(t, fsys.Path("demo-1.0.0.tgz"), map[string]string{
			"package.json": `{"name":"demo"}`,
			built:          "ok",
		})

		if err := appcontainer.ExtractNPMTarball(io.Discard, appcontainer.ExtractNPMTarballInput{Dir: fsys.Root}); err != nil {
			t.Fatalf("%s: ExtractNPMTarball: %v", built, err)
		}

		if body := fsys.ReadFile("package.json"); string(body) != `{"name":"demo"}` {
			t.Errorf("%s: package.json = %q", built, body)
		}

		if body := fsys.ReadFile(built); string(body) != "ok" {
			t.Errorf("%s = %q, want the built file as packed", built, body)
		}

		if _, err := os.Stat(fsys.Path("demo-1.0.0.tgz")); !os.IsNotExist(err) {
			t.Errorf("%s: tarball should be removed: %v", built, err)
		}
	}
}

// The image job unpacks the package of a container that names an npm artifact.
// Without the package the image would be built from the checkout alone, so a
// missing one stops the job and is named.
func TestExtractNPMTarball_NoTarballIsMissingInput(t *testing.T) {
	fsys := testfs.NewReal(t)

	err := appcontainer.ExtractNPMTarball(io.Discard, appcontainer.ExtractNPMTarballInput{Dir: fsys.Root})
	if !errors.Is(err, errs.ErrMissingInput) || !strings.Contains(err.Error(), "no npm package (*.tgz)") {
		t.Fatalf("err = %v, want a missing-input error naming the npm package", err)
	}
}

func TestExtractNPMTarball_StripsPackagePrefixIntoWorkingDir(t *testing.T) {
	fsys := testfs.NewReal(t)
	makeTarball(t, fsys.Path("package.tgz"), map[string]string{
		"file.txt": "hello\n",
	})

	if err := appcontainer.ExtractNPMTarball(io.Discard, appcontainer.ExtractNPMTarballInput{Dir: fsys.Root}); err != nil {
		t.Fatalf("ExtractNPMTarball: %v", err)
	}

	body := fsys.ReadFile("file.txt")
	if string(body) != "hello\n" {
		t.Errorf("file.txt = %q", body)
	}
}
