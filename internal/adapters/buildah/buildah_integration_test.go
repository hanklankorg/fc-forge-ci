// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build integration

package buildah_test

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/diggsweden/forge-ci/internal/adapters/buildah"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// hermeticContext writes a network-free build context: FROM scratch + COPY
// needs no base-image pull and no RUN, so it builds in an isolated vfs store
// without base-image network access or RUN privileges. Rootless Buildah still
// needs user namespaces to initialize the store, even with chroot isolation.
func hermeticContext(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "Containerfile"),
		[]byte("FROM scratch AS export\nCOPY hello.txt /hello.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return dir
}

// hermeticStore isolates buildah from the host and returns the global flags
// for a vfs store under the test's own root. testenv gives owned HOME, XDG and
// temporary directories with credentials and registry auth scrubbed; the
// containers, registries and storage configuration are owned files too, so no
// host containers.conf, registries.conf, storage.conf or auth file is read. A
// store reset is registered so t.TempDir cleanup isn't blocked by buildah's
// rootless layer files.
func hermeticStore(t *testing.T) []string {
	t.Helper()

	env := testenv.New(t)
	config := env.MkdirAll("containers")

	for name, body := range map[string]string{
		"containers.conf": "", "registries.conf": "", "storage.conf": "[storage]\ndriver = \"vfs\"\n", "auth.json": `{"auths":{}}`,
	} {
		if err := os.WriteFile(filepath.Join(config, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("CONTAINERS_CONF", filepath.Join(config, "containers.conf"))
	t.Setenv("CONTAINERS_REGISTRIES_CONF", filepath.Join(config, "registries.conf"))
	t.Setenv("CONTAINERS_STORAGE_CONF", filepath.Join(config, "storage.conf"))
	t.Setenv("REGISTRY_AUTH_FILE", filepath.Join(config, "auth.json"))
	t.Setenv("BUILDAH_ISOLATION", "chroot")

	store := t.TempDir()
	global := []string{
		"--storage-driver", "vfs",
		"--root", filepath.Join(store, "root"),
		"--runroot", filepath.Join(store, "runroot"),
	}
	t.Cleanup(func() {
		_ = exec.Command("buildah", append(append([]string{}, global...), "rmi", "--all", "--force")...).Run() //nolint:gosec,noctx // test-controlled args; cleanup outlives the test context.
	})

	return global
}

// requireBuildahStore checks the one prerequisite that may skip -- buildah on
// PATH -- and then proves the hermetic store works. It uses no adapter code,
// so once it passes, any build error is the product's and fails the test.
//
// Absence and breakage are deliberately not the same answer. A machine
// without buildah cannot produce this evidence and says so through
// evidence.RequireTool, which tallies the gap. A buildah that IS installed
// and cannot create a working container -- an unwritable root, a storage
// driver it may not use, any non-zero exit -- is a broken environment, and
// this used to skip for it too: the run reported "ok" having built nothing,
// which is the failure mode worth the most here, because a container tier
// that silently proves nothing is indistinguishable from one that works.
func requireBuildahStore(t *testing.T, global []string) {
	t.Helper()

	evidence.RequireTool(t, "buildah")

	cmd := exec.CommandContext(t.Context(), "buildah", append(append([]string{}, global...), "from", "scratch")...) //nolint:gosec // test-controlled args.
	out, stderr, err := buildahCommandOutput(cmd)
	if err != nil {
		t.Fatalf("buildah is installed but cannot create a container in the hermetic store "+
			"(storage driver, permissions, or configuration): %v", err)
	}
	if stderr != "" {
		t.Logf("buildah from scratch stderr:\n%s", stderr)
	}

	lines := strings.Fields(string(out))
	if len(lines) == 0 {
		t.Fatalf("buildah from scratch printed no container name")
	}

	runBuildah(t, global, "rm", lines[len(lines)-1])
}

func TestBuild_LoadThenLocal_WithRealBuildah(t *testing.T) {
	ctx := context.Background()
	global := hermeticStore(t)
	requireBuildahStore(t, global)
	adapter := &buildah.Adapter{Global: global}
	cdir := hermeticContext(t)
	cfile := filepath.Join(cdir, "Containerfile")

	// Load mode: build into the isolated store under a tag.
	if err := adapter.Build(ctx, container.BuildRequest{
		Context:         cdir,
		Containerfile:   cfile,
		Mode:            container.BuildModeLoad,
		ImageRef:        "forge-ci-buildtest:verify",
		SourceDateEpoch: "1700000000",
		Labels:          []string{"com.example.test=1"},
	}, os.Stderr); err != nil {
		t.Fatalf("load build: %v", err)
	}

	images := runBuildah(t, global, "images", "--format", "{{.Name}}:{{.Tag}}")
	if !strings.Contains(images, "forge-ci-buildtest:verify") {
		t.Errorf("built image not in store; images = %q", images)
	}

	// The requested metadata is on the image itself: the label, and the
	// creation time clamped to SOURCE_DATE_EPOCH 1700000000.
	metadata := runBuildah(t, global, "inspect", "--type", "image", "--format",
		`{{.OCIv1.Created.UTC.Format "2006-01-02T15:04:05Z07:00"}} {{index .OCIv1.Config.Labels "com.example.test"}}`, "forge-ci-buildtest:verify")
	if strings.TrimSpace(metadata) != "2023-11-14T22:13:20Z 1" {
		t.Errorf("image created/label = %q, want 2023-11-14T22:13:20Z 1", metadata)
	}

	// Local mode: export the target stage's filesystem to a directory.
	dest := t.TempDir()
	if err := adapter.Build(ctx, container.BuildRequest{
		Context:       cdir,
		Containerfile: cfile,
		Target:        "export",
		Mode:          container.BuildModeLocal,
		OutputDir:     dest,
	}, os.Stderr); err != nil {
		t.Fatalf("local-export build: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dest, "hello.txt"))
	if err != nil || strings.TrimSpace(string(body)) != "hi" {
		t.Errorf("exported file = %q, err = %v; want \"hi\"", body, err)
	}
}

// This is the hosted failure's environment, without a base image, registry,
// service, or RUN step. Verify the pinned image and layer semantics, not only
// that Buildah accepts the invocation.
func TestBuildTimestamp_InheritedEpoch_WithRealBuildah(t *testing.T) {
	global := hermeticStore(t)
	requireBuildahStore(t, global)
	cdir := hermeticContext(t)
	t.Setenv("SOURCE_DATE_EPOCH", "1792421899")
	adapter := &buildah.Adapter{Global: global}
	req := container.BuildRequest{
		Context: cdir, Containerfile: filepath.Join(cdir, "Containerfile"),
		Mode: container.BuildModePushByDigest, ImageRef: "registry.invalid/app",
		SourceDateEpoch: "1700000000",
	}
	// Confirm the exact original failure against the installed binary. Older
	// Buildah lacks this environment default and may successfully build instead.
	args := append(append([]string{}, global...), "build", "--isolation", "chroot", "--timestamp", req.SourceDateEpoch, cdir)
	out, err := exec.CommandContext(t.Context(), "buildah", args...).CombinedOutput() //nolint:gosec // test-owned paths and arguments.
	if err != nil && !strings.Contains(string(out), "timestamp and source-date-epoch would be ambiguous") {
		t.Fatalf("unexpected baseline failure: %v\n%s", err, out)
	}
	ambiguous := strings.Contains(string(out), "would be ambiguous")
	t.Logf("baseline inherited epoch plus --timestamp: err=%v; ambiguous=%t", err, ambiguous)

	var previous v1.Hash
	for attempt := range 2 {
		// Perturb the input mtime between builds. --timestamp must still produce
		// identical layer headers and image digests, not just pinned config time.
		mtime := time.Unix(1800000000+int64(attempt), 0)
		if err := os.Chtimes(filepath.Join(cdir, "hello.txt"), mtime, mtime); err != nil {
			t.Fatal(err)
		}
		dest := t.TempDir()
		if err := adapter.BuildToLayout(t.Context(), req, dest, os.Stderr); err != nil {
			t.Fatal(err)
		}
		requireLayoutMetadata(t, dest, "2023-11-14T22:13:20Z", nil)
		index, err := layout.ImageIndexFromPath(dest)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := index.IndexManifest()
		if err != nil {
			t.Fatal(err)
		}
		digest := manifest.Manifests[0].Digest
		if attempt > 0 && digest != previous {
			t.Errorf("rebuild changed digest: %s != %s", digest, previous)
		}
		previous = digest
		image, err := index.Image(digest)
		if err != nil {
			t.Fatal(err)
		}
		layers, err := image.Layers()
		if err != nil || len(layers) != 1 {
			t.Fatalf("layers=%d err=%v", len(layers), err)
		}
		body, err := layers[0].Uncompressed()
		if err != nil {
			t.Fatal(err)
		}
		reader := tar.NewReader(body)
		found := false
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = body.Close()
				t.Fatal(err)
			}
			if strings.TrimPrefix(header.Name, "/") == "hello.txt" {
				found = true
				if header.ModTime.Unix() != 1700000000 {
					t.Errorf("COPY layer mtime=%v, want requested epoch", header.ModTime)
				}
			}
		}
		_ = body.Close()
		if !found {
			t.Fatal("COPY file absent from layer")
		}
		t.Logf("normalized build %d digest: %s", attempt+1, digest)
	}

	// Exercise the exact failing local-export path with the inherited epoch.
	req.Mode, req.OutputDir = container.BuildModeLocal, t.TempDir()
	if err := adapter.Build(t.Context(), req, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(req.OutputDir, "hello.txt")); err != nil || string(body) != "hi\n" {
		t.Fatalf("local export=%q err=%v", body, err)
	}

	if err := adapter.BuildManifest(t.Context(), container.BuildPushManifestBuildRequest{
		Context: cdir, Containerfile: req.Containerfile, Manifest: "localhost/timestamp-regression:list",
		Platform: runtime.GOOS + "/" + runtime.GOARCH, SourceDateEpoch: req.SourceDateEpoch, TLSVerify: true,
	}, os.Stderr); err != nil {
		t.Fatalf("manifest build: %v", err)
	}
	dest := t.TempDir()
	if err := adapter.ExportLocalManifestToLayout(t.Context(), "localhost/timestamp-regression:list", dest, os.Stderr); err != nil {
		t.Fatal(err)
	}
	requireLayoutMetadata(t, dest, "2023-11-14T22:13:20Z", nil)

	// SOURCE_DATE_EPOCH as an explicit build argument is itself a Buildah mode
	// selector on newer versions. Scrubbing the inherited environment cannot
	// make it compatible with --timestamp; do not silently drop user arguments.
	req.BuildArgs = []string{"SOURCE_DATE_EPOCH=1700000000"}
	var log strings.Builder
	err = adapter.Build(t.Context(), req, &log)
	if ambiguous && (err == nil || !strings.Contains(log.String(), "would be ambiguous")) {
		t.Fatalf("explicit build-arg should retain Buildah's conflict: err=%v\n%s", err, log.String())
	}
	t.Logf("explicit SOURCE_DATE_EPOCH build-arg with --timestamp: err=%v; ambiguous=%t", err, strings.Contains(log.String(), "would be ambiguous"))
}

// TestBuildToLayout_PushByDigest_ToInProcessRegistry is the critical proof for
// the publish path: buildah builds to an OCI layout, the ggcr adapter pushes it
// by digest to an in-process registry over plain HTTP (loopback), the returned
// digest matches the stored manifest, and — crucially — NO tag is created
// (tagless, like build-push's push-by-digest, so there is nothing to clean up).
func TestBuildToLayout_PushByDigest_ToInProcessRegistry(t *testing.T) {
	ctx := context.Background()

	// The image-id file goes to the temporary directory. A private one keeps a
	// file some killed job left in the shared one out of the check below.
	t.Setenv("TMPDIR", t.TempDir())

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	repo := strings.TrimPrefix(srv.URL, "http://") + "/o/r"

	global := hermeticStore(t)
	requireBuildahStore(t, global)

	if before := runBuildah(t, global, "images", "--quiet"); strings.TrimSpace(before) != "" {
		t.Fatalf("hermetic store starts with images: %q", before)
	}

	adapter := &buildah.Adapter{Global: global}
	cdir := hermeticContext(t)

	layoutDir := t.TempDir()
	if err := adapter.BuildToLayout(ctx, container.BuildRequest{
		Context:         cdir,
		Containerfile:   filepath.Join(cdir, "Containerfile"),
		Mode:            container.BuildModePushByDigest,
		ImageRef:        repo,
		SourceDateEpoch: "1700000000",
		Labels:          []string{"org.opencontainers.image.source=https://example.invalid/app"},
	}, layoutDir, os.Stderr); err != nil {
		t.Fatalf("build to layout: %v", err)
	}

	// Cleanup as a runtime effect: the staged image is gone from the store and
	// the image-id file from the temporary directory.
	if after := runBuildah(t, global, "images", "--quiet"); strings.TrimSpace(after) != "" {
		t.Errorf("staged image left in the store: %q", after)
	}

	if leftovers, globErr := filepath.Glob(filepath.Join(os.TempDir(), "forge-ci-iid-*")); globErr != nil || len(leftovers) != 0 {
		t.Errorf("image-id files left behind: %v %v", leftovers, globErr)
	}

	requireLayoutMetadata(t, layoutDir, "2023-11-14T22:13:20Z", map[string]string{"org.opencontainers.image.source": "https://example.invalid/app"})

	digest, err := ociregistry.New().PushLayoutByDigest(ctx, layoutDir, repo)
	if err != nil {
		t.Fatalf("push by digest: %v", err)
	}

	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("returned digest = %q, want sha256:…", digest)
	}

	// Retrievable by digest.
	dref, err := name.NewDigest(repo+"@"+digest, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := remote.Get(dref, remote.WithContext(ctx)); err != nil {
		t.Fatalf("manifest not retrievable by digest: %v", err)
	}

	// Tagless: no tag was created.
	repoRef, err := name.NewRepository(repo, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	tags, err := remote.List(repoRef, remote.WithContext(ctx))
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}

	if len(tags) != 0 {
		t.Errorf("push-by-digest created tags %v; want none (tagless)", tags)
	}
}

// requireLayoutMetadata reads the one image in an OCI layout and requires its
// creation time and the given labels.
func requireLayoutMetadata(t *testing.T, layoutDir, created string, labels map[string]string) {
	t.Helper()

	index, err := layout.ImageIndexFromPath(layoutDir)
	if err != nil {
		t.Fatal(err)
	}

	manifest, err := index.IndexManifest()
	if err != nil || len(manifest.Manifests) != 1 {
		t.Fatalf("layout index = %+v, %v, want one image", manifest, err)
	}
	// A manifest-list export wraps its platform index in the layout index.
	if manifest.Manifests[0].MediaType.IsIndex() {
		index, err = index.ImageIndex(manifest.Manifests[0].Digest)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err = index.IndexManifest()
		if err != nil || len(manifest.Manifests) != 1 {
			t.Fatalf("platform index = %+v, %v, want one image", manifest, err)
		}
	}

	image, err := index.Image(manifest.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}

	config, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}

	if got := config.Created.UTC().Format(time.RFC3339); got != created {
		t.Errorf("layout image created = %s, want %s", got, created)
	}

	for key, want := range labels {
		if got := config.Config.Labels[key]; got != want {
			t.Errorf("layout image label %s = %q, want %q", key, got, want)
		}
	}
}

// TestBuild_BuildahSeesOnlyOwnedHostState plants a host HOME, temporary
// directory, XDG configuration with a containers storage.conf and a registry
// auth file before isolation, then runs both build paths. Nothing is written
// to or read from the planted host state: its directories hold only what was
// planted, and the builds succeed against the owned configuration.
func TestBuild_BuildahSeesOnlyOwnedHostState(t *testing.T) {
	host := t.TempDir()

	planted := map[string]string{
		"home/.config/containers/storage.conf": "[storage]\ndriver = \"overlay\"\ngraphroot = \"" + filepath.Join(host, "graph") + "\"\n",
		"auth.json":                            `{"auths":{"registry.invalid":{"auth":"aG9zdDpzZWNyZXQ="}}}`,
		"tmp/.keep":                            "",
	}
	for rel, body := range planted {
		path := filepath.Join(host, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("HOME", filepath.Join(host, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(host, "home", ".config"))
	t.Setenv("TMPDIR", filepath.Join(host, "tmp"))
	t.Setenv("REGISTRY_AUTH_FILE", filepath.Join(host, "auth.json"))
	t.Setenv("CONTAINERS_STORAGE_CONF", filepath.Join(host, "home", ".config", "containers", "storage.conf"))

	global := hermeticStore(t)
	requireBuildahStore(t, global)

	adapter := &buildah.Adapter{Global: global}
	cdir := hermeticContext(t)

	if err := adapter.Build(t.Context(), container.BuildRequest{
		Context: cdir, Containerfile: filepath.Join(cdir, "Containerfile"), Mode: container.BuildModeLoad, ImageRef: "forge-ci-hosttest:verify",
	}, io.Discard); err != nil {
		t.Fatalf("load build: %v", err)
	}

	if err := adapter.BuildToLayout(t.Context(), container.BuildRequest{
		Context: cdir, Containerfile: filepath.Join(cdir, "Containerfile"), Mode: container.BuildModePushByDigest, ImageRef: "registry.invalid/o/r",
	}, t.TempDir(), io.Discard); err != nil {
		t.Fatalf("build to layout: %v", err)
	}

	var found []string

	if err := filepath.WalkDir(host, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.IsDir() {
			rel, _ := filepath.Rel(host, path)
			found = append(found, rel)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	slices.Sort(found)

	want := slices.Sorted(maps.Keys(planted))
	if !slices.Equal(found, want) {
		t.Errorf("host state after the builds = %v, want only the planted %v", found, want)
	}

	if _, err := os.Stat(filepath.Join(host, "graph")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the host storage.conf graphroot was used: %v", err)
	}
}
