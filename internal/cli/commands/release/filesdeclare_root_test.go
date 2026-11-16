// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// declareBundle is the SBOM bundle of declareDist's release.
const declareBundle = "dist/app-1.2.3-sboms.tar.gz"

// declareDist is the dist of a build without build-tool metadata: one binary
// per platform directory, as `build go run` and `build cargo run` leave it, an
// SBOM, an evidence file and the release body. The job's environment names
// the repository and the release tag, as a release build job's does.
func declareDist(t *testing.T) *testenv.Env {
	t.Helper()

	env := provenanceRootEnv(t, "forgejo")
	t.Setenv("REPOSITORY", "org/app")
	t.Setenv("RELEASE_TAG", "v1.2.3")

	for path, body := range map[string]string{
		"dist/linux-amd64/app-linux-amd64": "amd64 binary\n",
		"dist/linux-arm64/app-linux-arm64": "arm64 binary\n",
		"dist/app.cdx.json":                "SBOM\n",
		"dist/plan.json":                   "evidence\n",
		"dist/release-notes.md":            "notes\n",
	} {
		env.MkdirAll(filepath.Dir(path))
		require.NoError(t, os.WriteFile(env.Path(path), []byte(body), 0o600))
	}

	return env
}

func declaredNames(t *testing.T, env *testenv.Env) map[string][]string {
	t.Helper()

	body, err := os.ReadFile(env.Path("dist/release-files.json"))
	require.NoError(t, err)

	var manifest map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &manifest))

	names := map[string][]string{}

	for _, section := range []string{"assets", "checksums", "sboms", "evidence", "provenance"} {
		var entries []struct {
			Path   string `json:"path"`
			Name   string `json:"name"`
			Source string `json:"source"`
		}
		require.NoError(t, json.Unmarshal(manifest[section], &entries), section)

		names[section] = []string{}

		for _, entry := range entries {
			require.NotEmpty(t, entry.Source)
			require.Equal(t, filepath.Base(entry.Path), entry.Name)
			names[section] = append(names[section], entry.Path)
		}
	}

	return names
}

// A producer names its files and gets the whole unsigned hand-off: the SBOM
// bundle, the checksums file over the assets and that bundle, and a manifest
// the signer's first two checks accept. The one SBOM is bundled too, under its
// file name, and is no release file itself. The bundle is named after the
// repository and the release tag of the job. A file two patterns name is
// declared once, and the release body is not a release file unless it is named.
func TestReleaseFilesRoot_DeclareWritesTheUnsignedHandOff(t *testing.T) {
	env := declareDist(t)

	require.NoError(t, runReleaseFilesRoot(t, "declare",
		"--asset", "dist/*/*", "--asset", "dist/linux-amd64/app-linux-amd64", "--sbom", "dist/*.cdx.json", "--evidence-file", "dist/plan.json"))

	require.Equal(t, map[string]string{"app.cdx.json": "SBOM\n"}, tarGzipEntries(t, env.Path(declareBundle)))

	bundle, err := os.ReadFile(env.Path(declareBundle))
	require.NoError(t, err)

	checksums, err := os.ReadFile(env.Path("dist/checksums.sha256"))
	require.NoError(t, err)
	require.Equal(t,
		fmt.Sprintf("%x  app-1.2.3-sboms.tar.gz\n%x  app-linux-amd64\n%x  app-linux-arm64\n",
			sha256.Sum256(bundle), sha256.Sum256([]byte("amd64 binary\n")), sha256.Sum256([]byte("arm64 binary\n"))),
		string(checksums))

	require.Equal(t, map[string][]string{
		"assets": {
			"dist/linux-amd64/app-linux-amd64", "dist/linux-arm64/app-linux-arm64", declareBundle,
			"dist/checksums.sha256", "dist/plan.json",
		},
		"checksums":  {"dist/checksums.sha256"},
		"sboms":      {declareBundle},
		"evidence":   {"dist/plan.json"},
		"provenance": {},
	}, declaredNames(t, env))

	require.NoError(t, runReleaseFilesRoot(t, "validate", "--unsigned"))
	require.NoError(t, runReleaseFilesRoot(t, "validate-checksums"))
}

// The flags name the bundle where the job's environment does not, and several
// SBOMs go into the one bundle in name order.
func TestReleaseFilesRoot_DeclareBundlesEverySBOMUnderTheNamedProject(t *testing.T) {
	env := declareDist(t)
	require.NoError(t, os.WriteFile(env.Path("dist/linux-amd64/worker.cdx.json"), []byte("worker SBOM\n"), 0o600))

	require.NoError(t, runReleaseFilesRoot(t, "declare", "--asset", "dist/*/app-*",
		"--sbom", "dist/linux-amd64/worker.cdx.json", "--sbom", "dist/app.cdx.json", "--project-name", "suite", "--version", "2.0.0"))

	require.Equal(t, []string{"dist/suite-2.0.0-sboms.tar.gz"}, declaredNames(t, env)["sboms"])
	require.Equal(t, map[string]string{"app.cdx.json": "SBOM\n", "worker.cdx.json": "worker SBOM\n"},
		tarGzipEntries(t, env.Path("dist/suite-2.0.0-sboms.tar.gz")))
}

// tarGzipEntries reads the regular entries of a tar.gz, content by name.
func tarGzipEntries(t *testing.T, path string) map[string]string {
	t.Helper()

	file, err := os.Open(path)
	require.NoError(t, err)

	defer func() { _ = file.Close() }()

	gz, err := gzip.NewReader(file)
	require.NoError(t, err)

	entries := map[string]string{}

	for reader := tar.NewReader(gz); ; {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}

		require.NoError(t, err)

		body, err := io.ReadAll(reader)
		require.NoError(t, err)

		entries[header.Name] = string(body)
	}
}

// The checksums file may carry the producer's own name, under the dist
// directory.
func TestReleaseFilesRoot_DeclareNamesTheChecksumsFile(t *testing.T) {
	env := declareDist(t)

	require.NoError(t, runReleaseFilesRoot(t, "declare", "--asset", "dist/*/*", "--checksums-file", "dist/SHA256SUMS"))
	require.Equal(t, []string{"dist/SHA256SUMS"}, declaredNames(t, env)["checksums"])
	require.NoFileExists(t, env.Path("dist/checksums.sha256"))
	require.NoError(t, runReleaseFilesRoot(t, "validate-checksums"))
}

// Every refusal leaves no manifest, and no checksums file or SBOM bundle of
// its own, so the corrected call is not refused for the first one's leftovers.
func TestReleaseFilesRoot_DeclareRefusals(t *testing.T) {
	for _, test := range []struct {
		name    string
		arrange func(t *testing.T, env *testenv.Env)
		args    []string
		want    error
		message string
		// kept is a file the arrangement placed, which the refusal must leave.
		kept string
	}{
		{
			name: "no asset", args: []string{"--sbom", "dist/app.cdx.json"},
			want: errs.ErrUsage, message: "at least one --asset is required",
		},
		{
			name: "a pattern that names no file", args: []string{"--asset", "dist/*/*", "--asset", "dist/*.tar.gz"},
			want: errs.ErrMissingInput, message: `--asset "dist/*.tar.gz" names no file`,
		},
		{
			name: "a pattern that names only directories", args: []string{"--asset", "dist/linux-*"},
			want: errs.ErrMissingInput, message: `--asset "dist/linux-*" names no file`,
		},
		{
			name: "an SBOM that does not exist", args: []string{"--asset", "dist/*/*", "--sbom", "dist/missing.cdx.json"},
			want: errs.ErrMissingInput, message: `--sbom "dist/missing.cdx.json" names no file`,
		},
		{
			name: "a file outside the dist directory",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				require.NoError(t, os.WriteFile(env.Path("outside.bin"), []byte("outside\n"), 0o600))
			},
			args: []string{"--asset", "outside.bin"},
			want: errs.ErrValidation, message: "artifact path must be under dist/: outside.bin",
		},
		{
			name: "a checksums file outside the dist directory", args: []string{"--asset", "dist/*/*", "--checksums-file", "SHA256SUMS"},
			want: errs.ErrValidation, message: "checksums file: artifact path must be under dist/: SHA256SUMS",
		},
		{
			name: "two files with one name",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				require.NoError(t, os.WriteFile(env.Path("dist/linux-arm64/app-linux-amd64"), []byte("misplaced\n"), 0o600))
			},
			args: []string{"--asset", "dist/*/*"},
			want: errs.ErrValidation, message: "duplicate release asset basename: app-linux-amd64",
		},
		{
			name: "an existing manifest",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				require.NoError(t, os.WriteFile(env.Path("dist/release-files.json"), []byte("{}\n"), 0o600))
			},
			args: []string{"--asset", "dist/*/*"},
			want: errs.ErrValidation, message: "release hand-off file already exists: dist/release-files.json",
			kept: "dist/release-files.json",
		},
		{
			name: "an existing checksums file",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				require.NoError(t, os.WriteFile(env.Path("dist/checksums.sha256"), []byte("stale\n"), 0o600))
			},
			args: []string{"--asset", "dist/*/*"},
			want: errs.ErrValidation, message: "release hand-off file already exists: dist/checksums.sha256",
			kept: "dist/checksums.sha256",
		},
		{
			name: "an existing SBOM bundle",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				require.NoError(t, os.WriteFile(env.Path(declareBundle), []byte("stale\n"), 0o600))
			},
			args: []string{"--asset", "dist/*/*", "--sbom", "dist/app.cdx.json"},
			want: errs.ErrValidation, message: "release hand-off file already exists: " + declareBundle,
			kept: declareBundle,
		},
		{
			name: "an SBOM and no release version",
			arrange: func(t *testing.T, _ *testenv.Env) {
				t.Helper()
				t.Setenv("RELEASE_TAG", "")
			},
			args: []string{"--asset", "dist/*/*", "--sbom", "dist/app.cdx.json"},
			want: errs.ErrUsage, message: "pass --project-name and --version",
		},
		{
			name: "an SBOM that is also an asset", args: []string{"--asset", "dist/*/*", "--asset", "dist/app.cdx.json", "--sbom", "dist/app.cdx.json"},
			want: errs.ErrValidation, message: "--sbom file is also an --asset: dist/app.cdx.json",
		},
		{
			name: "evidence that is already an asset", args: []string{"--asset", "dist/*/*", "--asset", "dist/plan.json", "--evidence-file", "dist/plan.json"},
			want: errs.ErrValidation, message: "evidence must be an additional release file: dist/plan.json",
		},
		{
			name: "a signature the build brought along",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				require.NoError(t, os.WriteFile(env.Path(declareBundle+".bundle"), []byte("planted\n"), 0o600))
			},
			args: []string{"--asset", "dist/*/*", "--sbom", "dist/app.cdx.json"},
			want: errs.ErrValidation, message: "unsigned dist must not hold " + declareBundle + ".bundle",
		},
		{
			name: "the image ledger as an asset",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				require.NoError(t, os.WriteFile(env.Path("dist/release-images.json"), []byte("[]\n"), 0o600))
			},
			args: []string{"--asset", "dist/release-images.json"},
			want: errs.ErrValidation, message: "release image ledger must not be a release file",
		},
		{
			name: "the release plan matched by a glob",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				require.NoError(t, os.WriteFile(env.Path("dist/release-plan.json"), []byte("{}\n"), 0o600))
			},
			args: []string{"--asset", "dist/*"},
			want: errs.ErrValidation, message: "release plan must not be a release file",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := declareDist(t)
			if test.arrange != nil {
				test.arrange(t, env)
			}

			err := runReleaseFilesRoot(t, append([]string{"declare"}, test.args...)...)
			require.ErrorIs(t, err, test.want)
			require.ErrorContains(t, err, test.message)

			for _, output := range []string{"dist/release-files.json", "dist/checksums.sha256", declareBundle, "SHA256SUMS"} {
				if output == test.kept {
					require.FileExists(t, env.Path(output))

					continue
				}

				require.NoFileExists(t, env.Path(output))
			}
		})
	}
}
