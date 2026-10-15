// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// platformIndex builds a linux/amd64 + linux/arm64 index and returns the
// digest of each platform image.
func platformIndex(t *testing.T) (v1.ImageIndex, map[string]v1.Hash) {
	t.Helper()

	index := v1.ImageIndex(empty.Index)
	digests := map[string]v1.Hash{}

	for _, arch := range []string{"amd64", "arm64"} {
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatalf("random image: %v", err)
		}

		if digests[arch], err = img.Digest(); err != nil {
			t.Fatal(err)
		}

		index = mutate.AppendManifests(index, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch}},
		})
	}

	return index, digests
}

// scanLayoutDigest returns the digest of the single image tagged "scan" in dir.
func scanLayoutDigest(t *testing.T, dir string) v1.Hash {
	t.Helper()

	idx, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		t.Fatalf("read layout: %v", err)
	}

	manifest, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}

	if len(manifest.Manifests) != 1 {
		t.Fatalf("scan layout holds %d entries, want exactly one image", len(manifest.Manifests))
	}

	if got := manifest.Manifests[0].Annotations[refNameAnnotation]; got != scanRefName {
		t.Errorf("scan layout image is tagged %q, want %q", got, scanRefName)
	}

	return manifest.Manifests[0].Digest
}

// taggedLayout writes index into a new layout tagged "scan", next to an
// untagged decoy index that must never be picked.
func taggedLayout(t *testing.T, index v1.ImageIndex) string {
	t.Helper()

	dir := t.TempDir()

	path, err := layout.Write(dir, empty.Index)
	if err != nil {
		t.Fatalf("layout.Write: %v", err)
	}

	decoy, _ := platformIndex(t)
	if err := path.AppendIndex(decoy); err != nil {
		t.Fatal(err)
	}

	if err := path.AppendIndex(index, layout.WithAnnotations(map[string]string{refNameAnnotation: scanRefName})); err != nil {
		t.Fatal(err)
	}

	return dir
}

// TestCopyDockerDigestToOCILayout_CopiesTheRequestedPlatform: the scan layout
// must hold the arm64 image of the pushed index, not the index or another
// platform, or the SBOM describes something the release does not ship.
func TestCopyDockerDigestToOCILayout_CopiesTheRequestedPlatform(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	repo := strings.TrimPrefix(srv.URL, "http://") + "/o/r"
	index, digests := platformIndex(t)

	indexDigest, err := index.Digest()
	if err != nil {
		t.Fatal(err)
	}

	ref, err := name.NewDigest(repo+"@"+indexDigest.String(), name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	if err := remote.WriteIndex(ref, index); err != nil {
		t.Fatalf("push index: %v", err)
	}

	dest := t.TempDir()
	if err := New().CopyDockerDigestToOCILayout(t.Context(), repo, indexDigest.String(), dest, "linux", "arm64"); err != nil {
		t.Fatalf("copy: %v", err)
	}

	if got := scanLayoutDigest(t, dest); got != digests["arm64"] {
		t.Errorf("copied %s, want the linux/arm64 image %s", got, digests["arm64"])
	}
}

// TestCopyOCILayoutToOCILayout_CopiesTheRequestedPlatformOfTheScanTag: only
// the index tagged "scan" is a source, and only its amd64 image is copied.
func TestCopyOCILayoutToOCILayout_CopiesTheRequestedPlatformOfTheScanTag(t *testing.T) {
	t.Parallel()

	index, digests := platformIndex(t)
	source := taggedLayout(t, index)

	dest := t.TempDir()
	if err := New().CopyOCILayoutToOCILayout(t.Context(), source, dest, "linux", "amd64"); err != nil {
		t.Fatalf("copy: %v", err)
	}

	if got := scanLayoutDigest(t, dest); got != digests["amd64"] {
		t.Errorf("copied %s, want the linux/amd64 image %s", got, digests["amd64"])
	}
}

// TestCopyToOCILayout_RefusesAMissingPlatform: an index without the requested
// platform is missing input, never a silent copy of some other image.
func TestCopyToOCILayout_RefusesAMissingPlatform(t *testing.T) {
	t.Parallel()

	index, _ := platformIndex(t)
	source := taggedLayout(t, index)

	err := New().CopyOCILayoutToOCILayout(t.Context(), source, t.TempDir(), "linux", "s390x")
	if !errors.Is(err, errs.ErrMissingInput) {
		t.Errorf("err = %v, want ErrMissingInput", err)
	}
}

// TestCopyToOCILayout_RequiresEveryInput: each refusal happens before anything
// is read, so no layout or registry is needed.
func TestCopyToOCILayout_RequiresEveryInput(t *testing.T) {
	t.Parallel()

	adapter := New()
	ctx := t.Context()

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"digest copy without a ref", func() error {
			return adapter.CopyDockerDigestToOCILayout(ctx, "", "sha256:abc", "/dst", "linux", "amd64")
		}},
		{"digest copy without a digest", func() error {
			return adapter.CopyDockerDigestToOCILayout(ctx, "ghcr.io/org/app", "", "/dst", "linux", "amd64")
		}},
		{"digest copy without a destination", func() error {
			return adapter.CopyDockerDigestToOCILayout(ctx, "ghcr.io/org/app", "sha256:abc", "", "linux", "amd64")
		}},
		{"digest copy without an os", func() error {
			return adapter.CopyDockerDigestToOCILayout(ctx, "ghcr.io/org/app", "sha256:abc", "/dst", "", "amd64")
		}},
		{"digest copy without an arch", func() error {
			return adapter.CopyDockerDigestToOCILayout(ctx, "ghcr.io/org/app", "sha256:abc", "/dst", "linux", "")
		}},
		{"layout copy without a source", func() error {
			return adapter.CopyOCILayoutToOCILayout(ctx, "", "/dst", "linux", "amd64")
		}},
		{"layout copy without a destination", func() error {
			return adapter.CopyOCILayoutToOCILayout(ctx, "/src", "", "linux", "amd64")
		}},
		{"layout copy without an os", func() error {
			return adapter.CopyOCILayoutToOCILayout(ctx, "/src", "/dst", "", "amd64")
		}},
		{"layout copy without an arch", func() error {
			return adapter.CopyOCILayoutToOCILayout(ctx, "/src", "/dst", "linux", "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := tc.call(); !errors.Is(err, errs.ErrUsage) {
				t.Errorf("err = %v, want ErrUsage before anything is read", err)
			}
		})
	}
}
