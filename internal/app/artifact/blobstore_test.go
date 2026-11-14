// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package artifact_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	appartifact "github.com/diggsweden/forge-ci/internal/app/artifact"
	domainartifact "github.com/diggsweden/forge-ci/internal/domain/artifact"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

const testPrefix = "images-blob"

// memoryStore is an in-memory run artifact store with the three roles the
// blob store needs. It refuses a second upload under an existing name, the
// way an immutable artifact store does.
type memoryStore struct {
	mu        sync.Mutex
	artifacts map[string]map[string][]byte
	uploads   []string
	// beforeUpload runs before an upload is accepted; it may add artifacts
	// to model a concurrent job, or fail the upload.
	beforeUpload func(name string) error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{artifacts: map[string]map[string][]byte{}}
}

func (m *memoryStore) UploadRunArtifact(_ context.Context, in provider.RunArtifactUpload) (provider.RunArtifactInfo, error) {
	if m.beforeUpload != nil {
		if err := m.beforeUpload(in.Name); err != nil {
			return provider.RunArtifactInfo{}, err
		}
	}

	entries, err := domainartifact.CollectUpload(in.Dir, in.Files, in.Paths, in.IncludeHidden)
	if err != nil {
		return provider.RunArtifactInfo{}, err
	}

	files := map[string][]byte{}

	var total int64

	for _, entry := range entries {
		body, readErr := os.ReadFile(entry.Abs)
		if readErr != nil {
			return provider.RunArtifactInfo{}, readErr
		}

		files[entry.RelPath] = body
		total += int64(len(body))
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.artifacts[in.Name]; exists {
		return provider.RunArtifactInfo{}, fmt.Errorf("artifact %q already exists: %w", in.Name, errs.ErrValidation)
	}

	m.artifacts[in.Name] = files
	m.uploads = append(m.uploads, in.Name)

	return provider.RunArtifactInfo{Name: in.Name, Bytes: total, FileCount: len(files)}, nil
}

func (m *memoryStore) DownloadRunArtifact(_ context.Context, in provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
	m.mu.Lock()
	files, ok := m.artifacts[in.Name]
	m.mu.Unlock()

	if !ok {
		return provider.RunArtifactInfo{}, fmt.Errorf("no artifact %q: %w", in.Name, errs.ErrReleaseNotFound)
	}

	var total int64

	for rel, body := range files {
		dest := filepath.Join(in.Dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return provider.RunArtifactInfo{}, err
		}

		// The forge adapters create downloads with the job's ordinary mode.
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			return provider.RunArtifactInfo{}, err
		}

		total += int64(len(body))
	}

	return provider.RunArtifactInfo{Name: in.Name, Bytes: total, FileCount: len(files)}, nil
}

func (m *memoryStore) ListRunArtifacts(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	names := make([]string, 0, len(m.artifacts))
	for name := range m.artifacts {
		names = append(names, name)
	}

	return names, nil
}

func (m *memoryStore) put(name, rel string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.artifacts[name] = map[string][]byte{rel: body}
}

func (m *memoryStore) blobUploads() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []string

	for _, name := range m.uploads {
		if strings.HasPrefix(name, testPrefix+"-") {
			out = append(out, name)
		}
	}

	return out
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)

	return hex.EncodeToString(sum[:])
}

// writeLayout writes a staged-layout directory: metadata, a hidden file, the
// layout's small files and its blobs, each named by its own digest.
func writeLayout(t *testing.T, dir string, blobs ...[]byte) {
	t.Helper()

	files := map[string][]byte{
		"forge-ci-image.json": []byte(`{"name":"app"}`),
		".hidden":             []byte("dot"),
		"layout/oci-layout":   []byte(`{"imageLayoutVersion":"1.0.0"}`),
		"layout/index.json":   []byte(`{"schemaVersion":2}`),
	}

	for _, body := range blobs {
		files["layout/blobs/sha256/"+sha256Hex(body)] = body
	}

	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()

	out := map[string]string{}

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}

		body, readErr := os.ReadFile(path)
		out[filepath.ToSlash(rel)] = string(body)

		return readErr
	})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func uploadLayout(t *testing.T, store *memoryStore, name, dir string) error {
	t.Helper()

	_, err := appartifact.UploadWithBlobStore(context.Background(), store, nil, nil, appartifact.BlobStoreUpload{
		Name: name, Dir: dir, IncludeHidden: true, RetentionDays: 1,
		Prefix:       testPrefix,
		MinBlobBytes: 8,
	})

	return err
}

func downloadLayout(store *memoryStore, name, dir string) (provider.RunArtifactInfo, error) {
	return appartifact.DownloadWithBlobStore(context.Background(), store, nil, nil, appartifact.BlobStoreDownload{
		Name: name, Dir: dir,
		Prefix: testPrefix,
	})
}

var (
	sharedBase = bytes.Repeat([]byte("shared base layer "), 64)
	flavourA   = bytes.Repeat([]byte("flavour a layer "), 32)
	flavourB   = bytes.Repeat([]byte("flavour b layer "), 32)
	tinyConfig = []byte("cfg")
)

func TestUploadWithBlobStore_StoresEachSharedBlobOncePerRun(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	root := t.TempDir()

	first := filepath.Join(root, "a-arch-amd64")
	second := filepath.Join(root, "b-arch-amd64")

	writeLayout(t, first, sharedBase, flavourA, tinyConfig)
	writeLayout(t, second, sharedBase, flavourB, tinyConfig)

	if err := uploadLayout(t, store, "images-layout-a-amd64", first); err != nil {
		t.Fatal(err)
	}

	if err := uploadLayout(t, store, "images-layout-b-amd64", second); err != nil {
		t.Fatal(err)
	}

	stored := store.blobUploads()
	if len(stored) != 3 {
		t.Fatalf("blob uploads = %v, want the shared base once and each flavour layer once", stored)
	}

	if got := len(domainartifact.BlobArtifactCandidates(testPrefix, sha256Hex(sharedBase), stored)); got != 1 {
		t.Errorf("shared base stored %d times, want once", got)
	}

	// Blobs below the threshold stay inside the layout artifact.
	layout := store.artifacts["images-layout-b-amd64"]
	if _, ok := layout["layout/blobs/sha256/"+sha256Hex(tinyConfig)]; !ok {
		t.Errorf("small blob left the layout artifact: %v", slices.Sorted(mapKeys(layout)))
	}

	if _, ok := layout["layout/blobs/sha256/"+sha256Hex(sharedBase)]; ok {
		t.Error("shared blob still uploaded inside the layout artifact")
	}

	if _, ok := layout[domainartifact.BlobManifestName]; !ok {
		t.Error("layout artifact carries no blob manifest")
	}
}

func mapKeys(m map[string][]byte) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range m {
			if !yield(key) {
				return
			}
		}
	}
}

func TestBlobStore_RoundTripRestoresTheExactDirectory(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeLayout(t, src, sharedBase, flavourA, tinyConfig)

	if err := uploadLayout(t, store, "images-layout-a-amd64", src); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(root, "out", "a-arch-amd64")

	info, err := downloadLayout(store, "images-layout-a-amd64", dest)
	if err != nil {
		t.Fatal(err)
	}

	want, got := readTree(t, src), readTree(t, dest)
	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Errorf("restored tree differs\nwant %v\n got %v", slices.Sorted(stringKeys(want)), slices.Sorted(stringKeys(got)))
	}

	if info.FileCount != len(want) {
		t.Errorf("FileCount = %d, want %d", info.FileCount, len(want))
	}

	leftovers, err := os.ReadDir(filepath.Join(root, "out"))
	if err != nil || len(leftovers) != 1 {
		t.Errorf("staging directories left beside the destination: %v (%v)", leftovers, err)
	}

	// A restored file gets the mode any file the job writes gets, so an
	// image built from it stays readable to a non-root user.
	reference := filepath.Join(root, "reference")
	if err := os.WriteFile(reference, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	wantMode, err := os.Stat(reference)
	if err != nil {
		t.Fatal(err)
	}

	err = filepath.WalkDir(dest, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}

		info, statErr := entry.Info()
		if statErr == nil && info.Mode().Perm() != wantMode.Mode().Perm() {
			t.Errorf("%s mode = %v, want %v", path, info.Mode().Perm(), wantMode.Mode().Perm())
		}

		return statErr
	})
	if err != nil {
		t.Fatal(err)
	}
}

func stringKeys(m map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range m {
			if !yield(key) {
				return
			}
		}
	}
}

func TestUploadWithBlobStore_ConcurrentIdenticalUploadIsSuccess(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	src := filepath.Join(t.TempDir(), "src")
	writeLayout(t, src, sharedBase)

	digestHex := sha256Hex(sharedBase)
	rival := domainartifact.BlobArtifactName(testPrefix, digestHex, "images-layout-rival-amd64")

	// The rival job stores the same blob between this job's existence check
	// and its upload, and the forge then refuses this one.
	store.beforeUpload = func(name string) error {
		if strings.HasPrefix(name, testPrefix+"-"+digestHex) {
			store.put(rival, digestHex, sharedBase)

			return fmt.Errorf("upload %q: HTTP 409: %w", name, errs.ErrValidation)
		}

		return nil
	}

	if err := uploadLayout(t, store, "images-layout-a-amd64", src); err != nil {
		t.Fatalf("an identical blob stored by another job must be success: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "dest")
	if _, err := downloadLayout(store, "images-layout-a-amd64", dest); err != nil {
		t.Fatal(err)
	}
}

func TestUploadWithBlobStore_RefusalWithoutAnotherCopyFails(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	src := filepath.Join(t.TempDir(), "src")
	writeLayout(t, src, sharedBase)

	store.beforeUpload = func(name string) error {
		if strings.HasPrefix(name, testPrefix+"-") {
			return fmt.Errorf("HTTP 413: %w", errs.ErrDependencyUnavailable)
		}

		return nil
	}

	err := uploadLayout(t, store, "images-layout-a-amd64", src)
	if err == nil || !strings.Contains(err.Error(), "sha256:"+sha256Hex(sharedBase)) {
		t.Fatalf("refused blob upload = %v, want an error naming the digest", err)
	}
}

func TestUploadWithBlobStore_RefusesABlobNamedForOtherBytes(t *testing.T) {
	t.Parallel()

	src := filepath.Join(t.TempDir(), "src")
	writeLayout(t, src, tinyConfig)

	forged := filepath.Join(src, "layout", "blobs", "sha256", sha256Hex(flavourA))
	if err := os.WriteFile(forged, sharedBase, 0o600); err != nil {
		t.Fatal(err)
	}

	err := uploadLayout(t, newMemoryStore(), "images-layout-a-amd64", src)
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("mislabelled blob = %v, want ErrValidation", err)
	}
}

func TestDownloadWithBlobStore_MissingBlobNamesTheDigest(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	src := filepath.Join(t.TempDir(), "src")
	writeLayout(t, src, sharedBase)

	if err := uploadLayout(t, store, "images-layout-a-amd64", src); err != nil {
		t.Fatal(err)
	}

	for _, name := range store.blobUploads() {
		delete(store.artifacts, name)
	}

	_, err := downloadLayout(store, "images-layout-a-amd64", filepath.Join(t.TempDir(), "dest"))
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "sha256:"+sha256Hex(sharedBase)) || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing blob = %v, want a validation error naming the digest", err)
	}
}

func TestDownloadWithBlobStore_DigestMismatchIsAHardError(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	src := filepath.Join(t.TempDir(), "src")
	writeLayout(t, src, sharedBase)

	if err := uploadLayout(t, store, "images-layout-a-amd64", src); err != nil {
		t.Fatal(err)
	}

	digestHex := sha256Hex(sharedBase)
	for _, name := range store.blobUploads() {
		store.put(name, digestHex, flavourA)
	}

	dest := filepath.Join(t.TempDir(), "dest")

	_, err := downloadLayout(store, "images-layout-a-amd64", dest)
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "sha256:"+digestHex) {
		t.Fatalf("mismatching blob = %v, want a validation error naming the digest", err)
	}

	if _, statErr := os.Stat(filepath.Join(dest, "layout", "blobs", "sha256", digestHex)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("mismatching bytes left in place: %v", statErr)
	}
}

func TestDownloadWithBlobStore_SkipsAPoisonedCopyForAGoodOne(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	src := filepath.Join(t.TempDir(), "src")
	writeLayout(t, src, sharedBase)

	if err := uploadLayout(t, store, "images-layout-a-amd64", src); err != nil {
		t.Fatal(err)
	}

	// Find an owner name whose blob artifact sorts before the good copy.
	digestHex := sha256Hex(sharedBase)
	good := store.blobUploads()[0]

	var poisoned string

	for i := 0; poisoned == ""; i++ {
		candidate := domainartifact.BlobArtifactName(testPrefix, digestHex, fmt.Sprintf("hostile-%d", i))
		if candidate < good {
			poisoned = candidate
		}
	}

	store.put(poisoned, digestHex, flavourA)

	dest := filepath.Join(t.TempDir(), "dest")
	if _, err := downloadLayout(store, "images-layout-a-amd64", dest); err != nil {
		t.Fatalf("a good copy beside a poisoned one must restore: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dest, "layout", "blobs", "sha256", digestHex))
	if err != nil || !bytes.Equal(body, sharedBase) {
		t.Fatalf("restored blob is not the verified one: %v", err)
	}
}

func TestBlobStore_RefusesUnusableRequests(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	ctx := context.Background()

	cases := []struct {
		name string
		run  func() error
	}{
		{"bad prefix", func() error {
			_, err := appartifact.UploadWithBlobStore(ctx, store, nil, nil, appartifact.BlobStoreUpload{
				Name: "x", Dir: ".", Prefix: "../x",
			})

			return err
		}},
		{"files instead of dir", func() error {
			_, err := appartifact.UploadWithBlobStore(ctx, store, nil, nil, appartifact.BlobStoreUpload{
				Name: "x", Files: []string{"a"}, Prefix: testPrefix,
			})

			return err
		}},
		{"pattern download", func() error {
			_, err := appartifact.DownloadWithBlobStore(ctx, store, nil, nil, appartifact.BlobStoreDownload{
				Pattern: "x-*", Dir: ".", Prefix: testPrefix,
			})

			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := tc.run(); !errors.Is(err, errs.ErrUsage) {
				t.Errorf("err = %v, want ErrUsage", err)
			}
		})
	}
}

func TestDownloadWithBlobStore_RequiresTheManifest(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	store.put("plain", "file.txt", []byte("x"))

	_, err := downloadLayout(store, "plain", filepath.Join(t.TempDir(), "dest"))
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("download without a manifest = %v, want ErrValidation", err)
	}
}

// A failed attempt may leave this job's own blob name holding part of the
// blob, and the forge lists it. A re-run must not take that name as stored:
// it uploads again, and when the store refuses, fails instead of publishing a
// layout whose blob no copy matches.
func TestUploadWithBlobStore_RerunDoesNotTrustItsOwnPartialCopy(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	src := filepath.Join(t.TempDir(), "src")
	writeLayout(t, src, sharedBase)

	digestHex := sha256Hex(sharedBase)
	own := domainartifact.BlobArtifactName(testPrefix, digestHex, "images-layout-a-amd64")
	store.put(own, digestHex, sharedBase[:len(sharedBase)/2])

	err := uploadLayout(t, store, "images-layout-a-amd64", src)
	if err == nil || !strings.Contains(err.Error(), "sha256:"+digestHex) {
		t.Fatalf("re-run over its own partial copy = %v, want an error naming the digest", err)
	}
}

// A re-run of a job whose earlier attempt stored the blob completely keeps
// that copy once it verifies, though the store refuses the name again.
func TestUploadWithBlobStore_RerunKeepsItsOwnVerifiedCopy(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	src := filepath.Join(t.TempDir(), "src")
	writeLayout(t, src, sharedBase)

	digestHex := sha256Hex(sharedBase)
	own := domainartifact.BlobArtifactName(testPrefix, digestHex, "images-layout-a-amd64")
	store.put(own, digestHex, sharedBase)

	if err := uploadLayout(t, store, "images-layout-a-amd64", src); err != nil {
		t.Fatalf("re-run over its own verified copy: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "dest")
	if _, err := downloadLayout(store, "images-layout-a-amd64", dest); err != nil {
		t.Fatal(err)
	}
}
