// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package artifact_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/artifact"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestSharedBlobHex_RecognisesOnlyLayoutBlobFiles(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]bool{
		"layout/blobs/sha256/" + digestA:                  true,
		"blobs/sha256/" + digestA:                         true,
		"layout/blobs/sha512/" + digestA:                  false,
		"layout/blobs/sha256/" + strings.ToUpper(digestA): false,
		"layout/blobs/sha256/" + digestA[:63]:             false,
		"layout/index.json":                               false,
		"sha256/" + digestA:                               false,
		"layout/blobs/sha256/" + digestA + "/x":           false,
		"layout/notblobs/sha256/" + digestA:               false,
		"deep/a/b/layout/blobs/sha256/" + digestA:         true,
		"layout/blobs/sha256/" + digestA[:62] + "zz":      false,
		"layout/blobs/sha256/" + digestA[:60] + "dead":    true,
		"layout/blobs/sha256/" + digestA[:60] + "DEAD":    false,
		"layout/blobs/sha256/" + digestA + "aa":           false,
		"layout/blobs/sha256/":                            false,
	} {
		_, got := artifact.SharedBlobHex(path)
		if got != want {
			t.Errorf("SharedBlobHex(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestBlobArtifactName_IsStablePerOwnerAndFoundAmongCandidates(t *testing.T) {
	t.Parallel()

	first := artifact.BlobArtifactName("images-blob", digestA, "images-layout-a-amd64")
	again := artifact.BlobArtifactName("images-blob", digestA, "images-layout-a-amd64")
	second := artifact.BlobArtifactName("images-blob", digestA, "images-layout-b-amd64")

	if first != again || first == second {
		t.Fatalf("names must be stable per owner and differ across owners: %q %q %q", first, again, second)
	}

	if err := artifact.ValidateName(first); err != nil {
		t.Fatal(err)
	}

	names := []string{
		second, first, first,
		"images-blob-" + digestA,                       // no owner tag
		"images-blob-" + digestA + "-short",            // not a tag
		"images-blob-" + digestA + "-ABCDEF0123456789", // uppercase tag
		"other-blob-" + digestA + "-0123456789abcdef",  // other store
		"images-layout-a-amd64",
	}

	got := artifact.BlobArtifactCandidates("images-blob", digestA, names)
	want := []string{first, second}
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

func TestValidateBlobPrefix(t *testing.T) {
	t.Parallel()

	for prefix, ok := range map[string]bool{
		"release-images-blob":   true,
		"a":                     true,
		"":                      false,
		"-x":                    false,
		"x/y":                   false,
		"x*":                    false,
		"x y":                   false,
		strings.Repeat("x", 97): false,
	} {
		err := artifact.ValidateBlobPrefix(prefix)
		if (err == nil) != ok || (err != nil && !errors.Is(err, errs.ErrUsage)) {
			t.Errorf("ValidateBlobPrefix(%q) = %v, want ok=%v", prefix, err, ok)
		}
	}
}

func TestParseBlobManifest(t *testing.T) {
	t.Parallel()

	blob := `{"path":"layout/blobs/sha256/` + digestA + `","digest":"sha256:` + digestA + `","size":3}`

	valid := `{"schema":"forge-ci.artifact-blobs/v1","blobs":[` + blob + `]}`

	manifest, err := artifact.ParseBlobManifest([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}

	if len(manifest.Blobs) != 1 || manifest.Blobs[0].Hex() != digestA {
		t.Fatalf("manifest = %+v", manifest)
	}

	otherDigest := strings.Repeat("b", 64)

	for name, body := range map[string]string{
		"unknown schema":  `{"schema":"v0","blobs":[]}`,
		"unknown field":   `{"schema":"forge-ci.artifact-blobs/v1","blobs":[],"extra":1}`,
		"digest not path": `{"schema":"forge-ci.artifact-blobs/v1","blobs":[{"path":"layout/blobs/sha256/` + digestA + `","digest":"sha256:` + otherDigest + `","size":3}]}`,
		"traversal":       `{"schema":"forge-ci.artifact-blobs/v1","blobs":[{"path":"../blobs/sha256/` + digestA + `","digest":"sha256:` + digestA + `","size":3}]}`,
		"absolute":        `{"schema":"forge-ci.artifact-blobs/v1","blobs":[{"path":"/blobs/sha256/` + digestA + `","digest":"sha256:` + digestA + `","size":3}]}`,
		"not a blob path": `{"schema":"forge-ci.artifact-blobs/v1","blobs":[{"path":"index.json","digest":"sha256:` + digestA + `","size":3}]}`,
		"negative size":   `{"schema":"forge-ci.artifact-blobs/v1","blobs":[{"path":"layout/blobs/sha256/` + digestA + `","digest":"sha256:` + digestA + `","size":-1}]}`,
		"other algorithm": `{"schema":"forge-ci.artifact-blobs/v1","blobs":[{"path":"layout/blobs/sha256/` + digestA + `","digest":"sha512:` + digestA + `","size":3}]}`,
		"listed twice":    `{"schema":"forge-ci.artifact-blobs/v1","blobs":[` + blob + `,` + blob + `]}`,
		"not json":        `{`,
	} {
		if _, err := artifact.ParseBlobManifest([]byte(body)); err == nil {
			t.Errorf("%s: manifest accepted", name)
		}
	}
}
