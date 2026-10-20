// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func executableLayer(t *testing.T, name, body string) v1.Layer {
	t.Helper()
	var data bytes.Buffer
	archive := tar.NewWriter(&data)
	if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(archive, body); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return static.NewLayer(data.Bytes(), types.OCIUncompressedLayer)
}

func TestLayoutExtractionUsesEffectiveImageLayersAndWhiteouts(t *testing.T) {
	t.Parallel()
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "latest bytes", true: "deleted by whiteout"}[deleted], func(t *testing.T) {
			t.Parallel()
			image, err := mutate.AppendLayers(empty.Image, executableLayer(t, "usr/bin/worker", "old build"), executableLayer(t, "usr/bin/worker", "retained scanned build"))
			if err != nil {
				t.Fatal(err)
			}
			if deleted {
				image, err = mutate.AppendLayers(image, executableLayer(t, "usr/bin/.wh.worker", ""))
				if err != nil {
					t.Fatal(err)
				}
			}
			directory, output := t.TempDir(), filepath.Join(t.TempDir(), "extracted")
			root, err := layout.Write(directory, empty.Index)
			if err != nil {
				t.Fatal(err)
			}
			if err := root.AppendImage(image); err != nil {
				t.Fatal(err)
			}
			err = appcontainer.ExtractLayoutFiles(t.Context(), NewAnonymous(), io.Discard, directory, output, []string{"/usr/bin/worker"})
			if deleted {
				if !errors.Is(err, errs.ErrMissingInput) {
					t.Fatalf("whiteout resurrected an earlier binary: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(output, "worker"))
			if err != nil || string(body) != "retained scanned build" {
				t.Fatalf("wrong image layer extracted: %q %v", body, err)
			}
		})
	}
}

func TestLayoutExtractionRefusesCorruptBlobsBeforeWritingOutput(t *testing.T) {
	t.Parallel()
	layer := executableLayer(t, "worker", "built bytes")
	image, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	directory, output := t.TempDir(), filepath.Join(t.TempDir(), "must-not-exist")
	root, err := layout.Write(directory, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.AppendImage(image); err != nil {
		t.Fatal(err)
	}
	digest, err := layer.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "blobs", digest.Algorithm, digest.Hex), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appcontainer.ExtractLayoutFiles(t.Context(), NewAnonymous(), io.Discard, directory, output, []string{"/worker"}); err == nil {
		t.Fatal("corrupt layout accepted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("bad layout wrote output: %v", err)
	}
}
