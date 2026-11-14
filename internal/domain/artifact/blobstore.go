// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The blob store shares the content-addressed files of a directory, the
// blobs/sha256/<digest> files of an OCI image layout, between the artifacts
// of one workflow run. Each blob travels once as its own artifact, named by
// its digest; the directory's own artifact carries everything else and a
// manifest of the blobs it needs. Images built from one base share most of
// their bytes, which a whole-layout artifact uploads again for every image.
const (
	// BlobManifestName is the manifest file at the root of an artifact
	// uploaded with a blob store. It is not hidden, so it never depends on
	// --include-hidden, and the download removes it after restoring the
	// blobs, so the directory reads exactly as it was uploaded.
	BlobManifestName = "forge-ci-blobs.json"
	// BlobManifestSchema identifies the manifest format.
	BlobManifestSchema = "forge-ci.artifact-blobs/v1"
	// MinSharedBlobBytes is the smallest blob moved to the store. Smaller
	// blobs, an image's manifest and config, are unique to the image and
	// cheaper inside its own artifact than as an artifact each.
	MinSharedBlobBytes int64 = 1 << 20
	// maxBlobPrefixLen bounds the prefix so a blob artifact name, prefix +
	// 64 + 16 + 2 characters, stays far below every forge's name limit.
	maxBlobPrefixLen = 96
	// blobOwnerLen is the length of the uploader tag in a blob artifact name.
	blobOwnerLen = 16
	// sha256HexLen is the length of a hex SHA-256 digest.
	sha256HexLen = 64
)

var (
	blobPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	lowerHexPattern   = regexp.MustCompile(`^[0-9a-f]+$`)
)

// BlobRef is one blob a directory needs from the store: where it belongs,
// relative to the directory root, and the digest and size it must have.
type BlobRef struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// Hex returns the blob's SHA-256 digest as bare lowercase hex.
func (r BlobRef) Hex() string { return strings.TrimPrefix(r.Digest, "sha256:") }

// BlobManifest lists the blobs an artifact left in the store.
type BlobManifest struct {
	Schema string    `json:"schema"`
	Blobs  []BlobRef `json:"blobs"`
}

// ValidateBlobPrefix accepts a plain artifact-name prefix: the names derived
// from it must be safe as artifact names and as directory names on disk.
func ValidateBlobPrefix(prefix string) error {
	if len(prefix) > maxBlobPrefixLen || !blobPrefixPattern.MatchString(prefix) {
		return fmt.Errorf("blob store prefix %q must be 1-%d characters of A-Z, a-z, 0-9, '.', '_' or '-', starting alphanumeric: %w",
			prefix, maxBlobPrefixLen, errs.ErrUsage)
	}

	return nil
}

// SharedBlobHex reports the digest a path names when it is the blob file of
// an OCI image layout, .../blobs/sha256/<64 lowercase hex>.
func SharedBlobHex(relPath string) (string, bool) {
	parts := strings.Split(relPath, "/")
	if len(parts) < 3 {
		return "", false
	}

	name := parts[len(parts)-1]
	if parts[len(parts)-3] != "blobs" || parts[len(parts)-2] != "sha256" || !isSHA256Hex(name) {
		return "", false
	}

	return name, true
}

// BlobArtifactName names the artifact one uploader stores a blob under. The
// owner, the uploader's own artifact name, enters as a short hash: two jobs
// racing to store the same blob write two artifacts and never one artifact
// twice, which a forge may refuse or, worse, merge.
func BlobArtifactName(prefix, digestHex, owner string) string {
	sum := sha256.Sum256([]byte(owner))

	return prefix + "-" + digestHex + "-" + hex.EncodeToString(sum[:])[:blobOwnerLen]
}

// BlobArtifactCandidates returns, sorted, the names among a run's artifacts
// that store the blob: <prefix>-<digest>-<owner tag>, from any uploader.
func BlobArtifactCandidates(prefix, digestHex string, names []string) []string {
	head := prefix + "-" + digestHex + "-"

	var out []string

	for _, name := range names {
		tag, ok := strings.CutPrefix(name, head)
		if ok && len(tag) == blobOwnerLen && lowerHexPattern.MatchString(tag) {
			out = append(out, name)
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// ParseBlobManifest decodes and checks a manifest: the known schema, each
// path safe under the directory and naming the blob file of its own digest,
// a size within the per-file limit, and no path listed twice.
func ParseBlobManifest(data []byte) (BlobManifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var manifest BlobManifest
	if err := dec.Decode(&manifest); err != nil {
		return BlobManifest{}, fmt.Errorf("blob manifest: %w: %w", err, errs.ErrMalformedInput)
	}

	if manifest.Schema != BlobManifestSchema {
		return BlobManifest{}, fmt.Errorf("blob manifest schema %q, want %q: %w", manifest.Schema, BlobManifestSchema, errs.ErrValidation)
	}

	seen := make(map[string]struct{}, len(manifest.Blobs))

	for _, ref := range manifest.Blobs {
		if err := validateBlobRef(ref); err != nil {
			return BlobManifest{}, err
		}

		if _, dup := seen[ref.Path]; dup {
			return BlobManifest{}, fmt.Errorf("blob manifest lists %q twice: %w", ref.Path, errs.ErrValidation)
		}

		seen[ref.Path] = struct{}{}
	}

	return manifest, nil
}

func validateBlobRef(ref BlobRef) error {
	digestHex, ok := strings.CutPrefix(ref.Digest, "sha256:")
	if !ok || !isSHA256Hex(digestHex) {
		return fmt.Errorf("blob manifest digest %q is not sha256:<64 lowercase hex>: %w", ref.Digest, errs.ErrValidation)
	}

	if pathHex, isBlob := SharedBlobHex(ref.Path); !isBlob || pathHex != digestHex {
		return fmt.Errorf("blob manifest path %q does not name blob %s: %w", ref.Path, ref.Digest, errs.ErrValidation)
	}

	if _, err := SafeJoin(".", ref.Path); err != nil {
		return err
	}

	if ref.Size < 0 || ref.Size > MaxFileBytes {
		return fmt.Errorf("blob manifest size %d of %s is outside 0..%d: %w", ref.Size, ref.Digest, MaxFileBytes, errs.ErrValidation)
	}

	return nil
}

// SHA256Reader returns the hex SHA-256 and length of everything r yields.
func SHA256Reader(r io.Reader) (string, int64, error) {
	hasher := sha256.New()

	n, err := io.Copy(hasher, r)
	if err != nil {
		return "", n, err
	}

	return hex.EncodeToString(hasher.Sum(nil)), n, nil
}

func isSHA256Hex(s string) bool {
	return len(s) == sha256HexLen && lowerHexPattern.MatchString(s)
}
