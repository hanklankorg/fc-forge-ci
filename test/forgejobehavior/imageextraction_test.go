// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestImageExtractionStepUsesRetainedBytesWithoutABuilder(t *testing.T) {
	t.Parallel()
	step := jobStep(t, ".github/workflows/publish-container.yml", "build-arch", "Extract binaries from retained image filesystem")
	box, env := engineSandbox(t, "image-file-extraction", map[string]string{"buildah": "#!/bin/sh\nexit 99\n", "docker": "#!/bin/sh\nexit 99\n"})
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	const expected = "executable bytes from the retained image"
	if err := archive.WriteHeader(&tar.Header{Name: "usr/bin/tool", Mode: 0o755, Size: int64(len(expected))}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte(expected)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	image, err := mutate.AppendLayers(empty.Image, static.NewLayer(buffer.Bytes(), types.OCIUncompressedLayer))
	if err != nil {
		t.Fatal(err)
	}
	root, err := layout.Write(filepath.Join(box.root, "image-layout"), empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.AppendImage(image); err != nil {
		t.Fatal(err)
	}
	result := box.run(step.Run, append(env, "RUNNER_TEMP="+box.root, "EXTRACT_BINARY_PATHS=/usr/bin/tool")...)
	if result.status != 0 {
		t.Fatalf("image extraction step failed: %d\n%s\n%s", result.status, result.stdout, result.stderr)
	}
	body, err := os.ReadFile(filepath.Join(box.root, "extracted-binaries", "tool"))
	if err != nil || string(body) != expected {
		t.Fatalf("workflow extracted different bytes: %q %v", body, err)
	}
}

func TestImageExtractionModeAdmission(t *testing.T) {
	t.Parallel()
	step := jobStep(t, ".github/workflows/publish-container.yml", "prep", "Validate binary extraction mode")
	for _, test := range []struct {
		name, target, paths, push string
		status                    int
	}{
		{"legacy stage", "export", "", "true", 0},
		{"retained image", "", "/usr/bin/tool", "true", 0},
		{"ambiguous sources", "export", "/usr/bin/tool", "true", 78},
		{"no retained layout", "", "/usr/bin/tool", "false", 78},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			box := newSandbox(t, "image-mode")
			result := box.run(step.Run, "EXTRACT_TARGET="+test.target, "EXTRACT_PATHS="+test.paths, "PUSH_IMAGE="+test.push)
			if result.status != test.status {
				t.Fatalf("status=%d want=%d stderr=%s", result.status, test.status, result.stderr)
			}
		})
	}
}
