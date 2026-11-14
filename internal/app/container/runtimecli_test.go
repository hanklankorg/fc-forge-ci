// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/stretchr/testify/require"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type runtimeCLIFixture struct {
	registryInputFixture

	body      []byte
	directory string
}

func (f *runtimeCLIFixture) ExportLayoutFilesystem(_ context.Context, directory string) (io.ReadCloser, error) {
	f.directory = directory

	var archive bytes.Buffer

	w := tar.NewWriter(&archive)
	if err := w.WriteHeader(&tar.Header{Name: "usr/local/bin/forge-ci", Mode: 0o755, Size: int64(len(f.body))}); err != nil {
		return nil, err
	}

	if _, err := w.Write(f.body); err != nil {
		return nil, err
	}

	if err := w.Close(); err != nil {
		return nil, err
	}

	return io.NopCloser(bytes.NewReader(archive.Bytes())), nil
}

func TestRuntimeCLIVerifiesFileAndBakedBytesWithoutExecution(t *testing.T) {
	t.Parallel()

	for _, image := range []bool{false, true} {
		for _, substituted := range []bool{false, true} {
			t.Run(fmt.Sprintf("image=%t/substituted=%t", image, substituted), func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				marker := filepath.Join(root, "executed")
				expected := []byte("#!/bin/sh\ntouch '" + marker + "'\nexit 99\n")

				require.NoError(t, os.MkdirAll(filepath.Join(root, ".engine/bin"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(root, ".engine/bin/forge-ci-linux-amd64.sha256"), fmt.Appendf(nil, "%x  bin/forge-ci\n", sha256.Sum256(expected)), 0o600))

				body := expected
				if substituted {
					body = []byte("substituted")
				}

				file := filepath.Join(root, "candidate")
				require.NoError(t, os.WriteFile(file, body, 0o700))

				manifest := []byte(`{"schemaVersion":2}`)
				digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
				fixture := &runtimeCLIFixture{manifest: manifest, image: domaincontainer.LayoutImage{Digest: digest, Platform: "linux/amd64"}, body: body}
				ref := ""

				if image {
					file = ""
					ref = "registry.example/acme/runtime@" + digest
				}

				err := appcontainer.VerifyRuntimeCLI(t.Context(), fixture, root, "amd64", file, ref)
				if substituted {
					require.ErrorContains(t, err, "do not match")
				} else {
					require.NoError(t, err)
				}

				require.NoFileExists(t, marker)

				if image {
					require.NoDirExists(t, filepath.Dir(fixture.directory))
					require.Equal(t, 1, fixture.copies)
				}
			})
		}
	}
}

func TestRuntimeCLIRejectsInvalidSourceAndArchitecture(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".engine/bin"), 0o700))

	fixture := &runtimeCLIFixture{}

	for _, record := range []string{"bad", strings.Repeat("a", 64) + "  wrong-name\n", strings.Repeat("a", 64) + "  bin/forge-ci\n" + strings.Repeat("b", 64) + "  extra\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, ".engine/bin/forge-ci-linux-amd64.sha256"), []byte(record), 0o600))
		require.Error(t, appcontainer.VerifyRuntimeCLI(t.Context(), fixture, root, "amd64", "", "registry.example/acme:latest"))
	}

	require.Error(t, appcontainer.VerifyRuntimeCLI(t.Context(), fixture, root, "../amd64", "file", ""))
	require.Zero(t, fixture.copies)
}
