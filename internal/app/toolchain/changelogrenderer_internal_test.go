// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package toolchain

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// rendererScript stands in for a renderer: it answers --version.
const rendererScript = "#!/bin/sh\necho \"renderer fixture $1\"\n"

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// rendererArchive is a tar.gz of regular files, in name order.
func rendererArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer

	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)

	for _, name := range slices.Sorted(maps.Keys(files)) {
		header := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(files[name]))}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}

		if _, err := archive.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}

	if err := errors.Join(archive.Close(), compressed.Close()); err != nil {
		t.Fatal(err)
	}

	return buffer.Bytes()
}

// rendererRelease serves one archive at the path a release keeps it under,
// and counts requests. Anything else is 404.
type rendererRelease struct {
	server   *httptest.Server
	requests atomic.Int32
	paths    []string
}

func serveRendererRelease(t *testing.T, path string, archive []byte) *rendererRelease {
	t.Helper()

	release := &rendererRelease{}
	release.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		release.requests.Add(1)
		release.paths = append(release.paths, request.URL.Path)

		if request.URL.Path != path || request.Header.Get("Authorization") != "" {
			http.NotFound(writer, request)

			return
		}

		_, _ = writer.Write(archive)
	}))
	t.Cleanup(release.server.Close)

	return release
}

// rendererFixture is a bin home and a path file below one owned root.
func rendererFixture(t *testing.T) (string, string, string) {
	t.Helper()

	root := changelogTempDir(t)
	t.Setenv("HOME", filepath.Join(root, "home"))

	pathFile := filepath.Join(root, "runner-path")
	writeChangelogCanary(t, pathFile, "/prior\n", 0o600)

	return root, filepath.Join(root, "bin"), pathFile
}

// thisArchPin puts digest in the pin of the architecture the test runs on and
// another valid digest in the other, so a swapped pair fails.
func thisArchPin(in InstallChangelogRendererInput, digest string) InstallChangelogRendererInput {
	other := strings.Repeat("0", 64)

	in.LinuxX64SHA256, in.LinuxARM64SHA256 = digest, other
	if runtime.GOARCH == goarchARM64 {
		in.LinuxX64SHA256, in.LinuxARM64SHA256 = other, digest
	}

	return in
}

// Each backend is one archive from its own release layout: the archive name
// and the member that is the executable differ per project. The executable
// lands in the bin home, the bin home on the runner path, and the request
// carries no credential.
func TestInstallChangelogRenderer_InstallsThePinnedArchive(t *testing.T) {
	machine := map[string]string{goarchAMD64: "x86_64", goarchARM64: "aarch64"}[runtime.GOARCH]
	if machine == "" {
		t.Skipf("no renderer archive for %s", runtime.GOARCH)
	}

	for _, testCase := range []struct {
		backend, version, path, member string
	}{
		{gitChglogBin, "0.15.4", "/v0.15.4/git-chglog_0.15.4_linux_" + runtime.GOARCH + ".tar.gz", "git-chglog"},
		{gitCliffBin, "2.10.0", "/v2.10.0/git-cliff-2.10.0-" + machine + "-unknown-linux-musl.tar.gz", "git-cliff-2.10.0/git-cliff"},
	} {
		t.Run(testCase.backend, func(t *testing.T) {
			_, binHome, pathFile := rendererFixture(t)
			// A decoy of the same base name elsewhere in the archive is not the renderer.
			archive := rendererArchive(t, map[string]string{
				testCase.member:               rendererScript,
				"docs/" + testCase.backend:    "decoy\n",
				"README.md":                   "readme\n",
				testCase.member + "-mangen":   "sibling\n",
				"other/" + testCase.member:    "decoy\n",
				testCase.backend + "/unknown": "decoy\n",
			})
			release := serveRendererRelease(t, testCase.path, archive)
			t.Setenv("GITHUB_TOKEN", "must-not-be-sent")

			var out bytes.Buffer

			err := InstallChangelogRenderer(context.Background(), release.server.Client(), &out, thisArchPin(InstallChangelogRendererInput{
				Backend: testCase.backend, Version: testCase.version, BaseURL: release.server.URL + "/",
				BinHome: binHome, PathFile: pathFile,
			}, sha256Hex(archive)))
			if err != nil {
				t.Fatalf("install: %v (requested %q)", err, release.paths)
			}

			installed := filepath.Join(binHome, testCase.backend)
			if body, readErr := os.ReadFile(installed); readErr != nil || string(body) != rendererScript { //nolint:gosec // test-owned path.
				t.Fatalf("installed %q, %v", body, readErr)
			}

			if info, statErr := os.Stat(installed); statErr != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("installed mode %v, %v", info, statErr)
			}

			if entries, _ := os.ReadDir(binHome); len(entries) != 1 {
				t.Errorf("the bin home holds %d entries, want the renderer alone", len(entries))
			}

			assertChangelogFile(t, pathFile, "/prior\n"+binHome+"\n", 0o600)

			if release.requests.Load() != 1 || !strings.Contains(out.String(), "renderer fixture --version") {
				t.Errorf("requests %d, output %q", release.requests.Load(), out.String())
			}
		})
	}
}

// The checksum is the trust anchor: an archive that does not hash to the pin
// of this architecture installs nothing and exports nothing.
func TestInstallChangelogRenderer_RefusesAnArchiveThatDoesNotMatchItsPin(t *testing.T) {
	root, binHome, pathFile := rendererFixture(t)
	archive := rendererArchive(t, map[string]string{gitChglogBin: rendererScript})
	release := serveRendererRelease(t, "/v0.15.4/git-chglog_0.15.4_linux_"+runtime.GOARCH+".tar.gz", archive)

	in := thisArchPin(InstallChangelogRendererInput{
		Backend: gitChglogBin, Version: "0.15.4", BaseURL: release.server.URL, BinHome: binHome, PathFile: pathFile,
	}, sha256Hex(archive))
	// The pair swapped: the digest of this archive stands for the other architecture.
	in.LinuxX64SHA256, in.LinuxARM64SHA256 = in.LinuxARM64SHA256, in.LinuxX64SHA256

	unchanged := changelogUnchanged(t, root)

	err := InstallChangelogRenderer(context.Background(), release.server.Client(), nil, in)
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}

	unchanged()
}

// A verified archive that does not hold the executable where the release
// layout puts it is malformed, and a release that is not there is an
// unavailable dependency that no retry will produce.
func TestInstallChangelogRenderer_ArchiveAndDownloadFailures(t *testing.T) {
	path := "/v0.15.4/git-chglog_0.15.4_linux_" + runtime.GOARCH + ".tar.gz"

	t.Run("no executable at the member path", func(t *testing.T) {
		root, binHome, pathFile := rendererFixture(t)
		archive := rendererArchive(t, map[string]string{"bin/git-chglog": rendererScript})
		release := serveRendererRelease(t, path, archive)
		unchanged := changelogUnchanged(t, root)

		err := InstallChangelogRenderer(context.Background(), release.server.Client(), nil, thisArchPin(InstallChangelogRendererInput{
			Backend: gitChglogBin, Version: "0.15.4", BaseURL: release.server.URL, BinHome: binHome, PathFile: pathFile,
		}, sha256Hex(archive)))
		if !errors.Is(err, errs.ErrMalformedInput) {
			t.Fatalf("err = %v, want ErrMalformedInput", err)
		}

		unchanged()
	})

	t.Run("no such release", func(t *testing.T) {
		root, binHome, pathFile := rendererFixture(t)
		release := serveRendererRelease(t, "/elsewhere", nil)
		unchanged := changelogUnchanged(t, root)

		err := InstallChangelogRenderer(context.Background(), release.server.Client(), nil, thisArchPin(InstallChangelogRendererInput{
			Backend: gitChglogBin, Version: "0.15.4", BaseURL: release.server.URL, BinHome: binHome, PathFile: pathFile,
		}, strings.Repeat("1", 64)))
		if !errors.Is(err, errs.ErrDependencyUnavailable) || release.requests.Load() != 1 {
			t.Fatalf("err = %v after %d request(s), want one refused download", err, release.requests.Load())
		}

		unchanged()
	})
}

// Every local refusal precedes the download: the transport is never reached,
// and nothing under the fixture changes.
func TestInstallChangelogRenderer_LocalRefusalsPrecedeTheDownload(t *testing.T) {
	valid := strings.Repeat("a", 64)

	for name, testCase := range map[string]struct {
		edit func(in *InstallChangelogRendererInput, binHome string)
		want error
	}{
		"an unknown backend":          {func(in *InstallChangelogRendererInput, _ string) { in.Backend = "changelogger" }, errs.ErrValidation},
		"a tool selector as backend":  {func(in *InstallChangelogRendererInput, _ string) { in.Backend = "aqua:attacker/tool" }, errs.ErrValidation},
		"a backend with padding":      {func(in *InstallChangelogRendererInput, _ string) { in.Backend = " git-cliff" }, errs.ErrValidation},
		"no version":                  {func(in *InstallChangelogRendererInput, _ string) { in.Version = "" }, errs.ErrUsage},
		"a version with a v":          {func(in *InstallChangelogRendererInput, _ string) { in.Version = "v2.10.0" }, errs.ErrUsage},
		"a version range":             {func(in *InstallChangelogRendererInput, _ string) { in.Version = "2.10" }, errs.ErrUsage},
		"a version with a path":       {func(in *InstallChangelogRendererInput, _ string) { in.Version = "2.10.0/../x" }, errs.ErrUsage},
		"one checksum missing":        {func(in *InstallChangelogRendererInput, _ string) { in.LinuxARM64SHA256 = "" }, errs.ErrUsage},
		"an uppercase checksum":       {func(in *InstallChangelogRendererInput, _ string) { in.LinuxX64SHA256 = strings.Repeat("A", 64) }, errs.ErrUsage},
		"a prefixed checksum":         {func(in *InstallChangelogRendererInput, _ string) { in.LinuxX64SHA256 = "sha256:" + valid }, errs.ErrUsage},
		"no path file":                {func(in *InstallChangelogRendererInput, _ string) { in.PathFile = "" }, errs.ErrUsage},
		"a base URL with credentials": {func(in *InstallChangelogRendererInput, _ string) { in.BaseURL = "https://user:pass@mirror.example/x" }, errs.ErrUsage},
		"a base URL with a query":     {func(in *InstallChangelogRendererInput, _ string) { in.BaseURL = "https://mirror.example/x?token=1" }, errs.ErrUsage},
		"a base URL that is not HTTP": {func(in *InstallChangelogRendererInput, _ string) { in.BaseURL = "file:///srv/mirror" }, errs.ErrUsage},
		"the path file as destination": {func(in *InstallChangelogRendererInput, binHome string) {
			in.PathFile = filepath.Join(binHome, gitCliffBin)
		}, errs.ErrValidation},
		"a directory at the destination": {
			func(_ *InstallChangelogRendererInput, binHome string) {
				if err := os.MkdirAll(filepath.Join(binHome, gitCliffBin), 0o700); err != nil {
					panic(err)
				}
			}, errs.ErrValidation,
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, binHome, pathFile := rendererFixture(t)

			var requests atomic.Int32

			blockChangelogHTTP(t, func(*http.Request) (*http.Response, error) {
				requests.Add(1)

				return &http.Response{StatusCode: http.StatusForbidden, Body: http.NoBody, Header: make(http.Header)}, nil
			})

			in := InstallChangelogRendererInput{
				Backend: gitCliffBin, Version: "2.10.0", LinuxX64SHA256: valid, LinuxARM64SHA256: valid,
				BinHome: binHome, PathFile: pathFile,
			}
			testCase.edit(&in, binHome)

			unchanged := changelogUnchanged(t, root)

			if err := InstallChangelogRenderer(context.Background(), nil, nil, in); !errors.Is(err, testCase.want) {
				t.Fatalf("err = %v, want %v", err, testCase.want)
			}

			if requests.Load() != 0 {
				t.Errorf("%d request(s) were sent before the refusal", requests.Load())
			}

			unchanged()
		})
	}
}

// A link left at the renderer's name, by an earlier run or by anyone, is
// replaced by the verified executable. Nothing is written through it.
func TestInstallChangelogRenderer_ReplacesALinkWithoutWritingThroughIt(t *testing.T) {
	root, binHome, pathFile := rendererFixture(t)
	archive := rendererArchive(t, map[string]string{gitChglogBin: rendererScript})
	release := serveRendererRelease(t, "/v0.15.4/git-chglog_0.15.4_linux_"+runtime.GOARCH+".tar.gz", archive)

	target := filepath.Join(root, "elsewhere", "target")
	writeChangelogCanary(t, target, "not the renderer\n", 0o600)

	if err := os.MkdirAll(binHome, 0o700); err != nil {
		t.Fatal(err)
	}

	changelogSymlink(t, target, filepath.Join(binHome, gitChglogBin))

	err := InstallChangelogRenderer(context.Background(), release.server.Client(), nil, thisArchPin(InstallChangelogRendererInput{
		Backend: gitChglogBin, Version: "0.15.4", BaseURL: release.server.URL, BinHome: binHome, PathFile: pathFile,
	}, sha256Hex(archive)))
	if err != nil {
		t.Fatal(err)
	}

	assertChangelogFile(t, target, "not the renderer\n", 0o600)

	if info, statErr := os.Lstat(filepath.Join(binHome, gitChglogBin)); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("the destination is %v (%v), want the installed regular file", info, statErr)
	}
}
