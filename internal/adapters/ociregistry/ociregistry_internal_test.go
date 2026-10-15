// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ociregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestCraneOptions_LeaveRetryOwnershipWithGoContainerRegistry(t *testing.T) {
	t.Parallel()

	options := crane.GetOptions(New().craneOpts(context.Background(), "ghcr.io/example/image:tag")...)
	if options.Transport != remote.DefaultTransport {
		t.Fatalf("transport = %T, want go-containerregistry default retry transport", options.Transport)
	}
}

// TestPushLayoutByDigest_PushesRetrievablyAndCreatesNoTag writes a random image to an OCI layout, pushes it
// tagless by digest to an in-process registry, and asserts the manifest is
// retrievable at that exact digest with NO tag created — the daemonless
// push-by-digest the buildah build path relies on (no ephemeral tag to clean up).
func TestPushLayoutByDigest_PushesRetrievablyAndCreatesNoTag(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	repo := strings.TrimPrefix(srv.URL, "http://") + "/o/r"

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}

	layoutDir := t.TempDir()

	p, err := layout.Write(layoutDir, empty.Index)
	if err != nil {
		t.Fatalf("layout.Write: %v", err)
	}

	if err = p.AppendImage(img); err != nil {
		t.Fatalf("AppendImage: %v", err)
	}

	want, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}

	got, err := New().PushLayoutByDigest(t.Context(), layoutDir, repo)
	if err != nil {
		t.Fatalf("PushLayoutByDigest: %v", err)
	}

	if got != want.String() {
		t.Errorf("returned digest = %s, want %s", got, want)
	}

	dref, err := name.NewDigest(repo+"@"+got, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = remote.Get(dref, remote.WithContext(t.Context())); err != nil {
		t.Fatalf("manifest not retrievable by digest: %v", err)
	}

	repoRef, err := name.NewRepository(repo, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	tags, err := remote.List(repoRef, remote.WithContext(t.Context()))
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}

	if len(tags) != 0 {
		t.Errorf("push-by-digest created tags %v; want none (tagless)", tags)
	}
}

// TestAllLoopback_IsTrueOnlyWhenEveryRefTargetsLoopback pins the security-relevant predicate: plain-HTTP (insecure)
// is enabled ONLY when every ref targets a loopback host. A real registry —
// or a mix — must stay on HTTPS so credentials never travel in clear text.
func TestAllLoopback_IsTrueOnlyWhenEveryRefTargetsLoopback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		refs []string
		want bool
	}{
		{"localhost", []string{"localhost:5000/o/r:tag"}, true},
		{"loopback_ip", []string{"127.0.0.1:5000/o/r@sha256:" + strings.Repeat("a", 64)}, true},
		{"real_registry", []string{"ghcr.io/o/r:tag"}, false},
		{"mixed_loopback_and_real", []string{"127.0.0.1:5000/o/r:t", "ghcr.io/o/r:t"}, false},
		{"empty", nil, false},
		{"unparsable", []string{"::not a ref::"}, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := allLoopback(testCase.refs); got != testCase.want {
				t.Errorf("allLoopback(%v) = %v, want %v", testCase.refs, got, testCase.want)
			}
		})
	}
}

// TestResolveDigestAndCopyTag_SecondTagResolvesToTheSourceDigest round-trips the adapter against an in-process
// registry: a seeded image's digest resolves, and CopyTag points a second tag
// at the same digest — daemonless, no docker.
func TestResolveDigestAndCopyTag_SecondTagResolvesToTheSourceDigest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	repo := strings.TrimPrefix(srv.URL, "http://") + "/o/r"
	src := repo + ":src"

	img, err := random.Image(512, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}

	ref, err := name.ParseReference(src, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	if err = remote.Write(ref, img, remote.WithContext(t.Context())); err != nil {
		t.Fatalf("seed image: %v", err)
	}

	want, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}

	adapter := New()

	got, err := adapter.ResolveDigest(t.Context(), src)
	if err != nil {
		t.Fatalf("ResolveDigest: %v", err)
	}

	if got != want.String() {
		t.Errorf("ResolveDigest = %s, want %s", got, want)
	}

	dest := repo + ":dest"
	if err = adapter.CopyTag(t.Context(), src, dest); err != nil {
		t.Fatalf("CopyTag: %v", err)
	}

	gotDest, err := adapter.ResolveDigest(t.Context(), dest)
	if err != nil {
		t.Fatalf("ResolveDigest(dest): %v", err)
	}

	if gotDest != want.String() {
		t.Errorf("after CopyTag, dest digest = %s, want %s", gotDest, want)
	}
}

func TestResolveDigest_MissingTagIsMissingInput(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	repo := strings.TrimPrefix(srv.URL, "http://") + "/o/r"

	_, err := New().ResolveDigest(t.Context(), repo+":missing")
	if !errors.Is(err, errs.ErrMissingInput) {
		t.Fatalf("ResolveDigest missing tag error = %v, want ErrMissingInput", err)
	}
}

// TestMergeManifest_AdvertisesEveryPlatformChild assembles a multi-platform index from two per-arch images
// and asserts the written index advertises both children with the correct
// platforms — the daemonless replacement for `docker buildx imagetools create`.
func TestMergeManifest_AdvertisesEveryPlatformChild(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	repo := strings.TrimPrefix(srv.URL, "http://") + "/o/r"

	platforms := []struct {
		os, arch, hex string
	}{
		{os: osLinux, arch: "amd64"},
		{os: osLinux, arch: "arm64"},
	}

	for i := range platforms {
		img, err := random.Image(512, 1)
		if err != nil {
			t.Fatalf("random image: %v", err)
		}

		config, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}

		config = config.DeepCopy()
		config.OS = platforms[i].os
		config.Architecture = platforms[i].arch

		img, err = mutate.ConfigFile(img, config)
		if err != nil {
			t.Fatalf("set platform: %v", err)
		}

		ref, err := name.ParseReference(repo+":"+platforms[i].arch, name.Insecure)
		if err != nil {
			t.Fatal(err)
		}

		if err = remote.Write(ref, img, remote.WithContext(t.Context())); err != nil {
			t.Fatalf("seed %s: %v", platforms[i].arch, err)
		}

		digest, err := img.Digest()
		if err != nil {
			t.Fatal(err)
		}

		platforms[i].hex = digest.Hex
	}

	tag := repo + ":multi"
	if err := New().MergeManifest(t.Context(), repo, []string{platforms[0].hex, platforms[1].hex}, []string{tag}); err != nil {
		t.Fatalf("MergeManifest: %v", err)
	}

	tagRef, err := name.ParseReference(tag, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	index, err := remote.Index(tagRef, remote.WithContext(t.Context()))
	if err != nil {
		t.Fatalf("fetch index: %v", err)
	}

	manifest, err := index.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}

	if len(manifest.Manifests) != 2 {
		t.Fatalf("index has %d manifests, want 2", len(manifest.Manifests))
	}

	got := map[string]string{}

	for _, child := range manifest.Manifests {
		if child.Platform == nil {
			t.Fatalf("child %s has no platform descriptor", child.Digest)
		}

		got[child.Digest.Hex] = child.Platform.OS + "/" + child.Platform.Architecture
	}

	for _, want := range platforms {
		if got[want.hex] != want.os+"/"+want.arch {
			t.Errorf("child %s platform = %q, want %s/%s", want.hex, got[want.hex], want.os, want.arch)
		}
	}
}

// TestManifest_ReturnsRawJSONNamingTheConfigDigest fetches the raw manifest the registry serves and checks it is
// the real document (valid JSON referencing the image's config digest).
func TestManifest_ReturnsRawJSONNamingTheConfigDigest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	repo := strings.TrimPrefix(srv.URL, "http://") + "/o/r"

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}

	ref, err := name.ParseReference(repo+":x", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	if err = remote.Write(ref, img, remote.WithContext(t.Context())); err != nil {
		t.Fatalf("seed image: %v", err)
	}

	raw, err := New().Manifest(t.Context(), repo+":x")
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}

	if !json.Valid(raw) {
		t.Fatalf("manifest is not valid JSON: %s", raw)
	}

	config, err := img.ConfigName()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(raw), config.Hex) {
		t.Errorf("manifest does not reference config digest %s:\n%s", config.Hex, raw)
	}
}

func TestLabels_FetchesImageConfigLabels(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	repo := strings.TrimPrefix(srv.URL, "http://") + "/o/r"

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}

	config, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}

	config = config.DeepCopy()
	config.Config.Labels = map[string]string{
		"org.opencontainers.image.revision": "abc123",
		"org.opencontainers.image.version":  "v1.2.3",
	}

	img, err = mutate.ConfigFile(img, config)
	if err != nil {
		t.Fatalf("set labels: %v", err)
	}

	ref, err := name.ParseReference(repo+":x", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	if err = remote.Write(ref, img, remote.WithContext(t.Context())); err != nil {
		t.Fatalf("seed image: %v", err)
	}

	labels, err := New().Labels(t.Context(), repo+":x")
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}

	if labels["org.opencontainers.image.revision"] != "abc123" || labels["org.opencontainers.image.version"] != "v1.2.3" {
		t.Fatalf("labels = %v", labels)
	}
}

// A source that does not exist is a permanent condition, and the exit ladder
// must say so. Reported as "the dependency is unavailable" it becomes exit 69,
// which tells CI to retry something that can never succeed — the failure mode
// that hid a real defect: on a forge that drops untagged manifests, rolling back
// a moving tag retried forever instead of reporting the image was gone.
func TestCopyTag_MissingSourceIsMissingInputNotUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	repo := strings.TrimPrefix(srv.URL, "http://") + "/o/r"

	err := New().CopyTag(t.Context(), repo+":absent", repo+":dest")
	if err == nil {
		t.Fatal("CopyTag from a missing source returned no error")
	}

	if errors.Is(err, errs.ErrDependencyUnavailable) {
		t.Errorf("CopyTag classified a missing source as the dependency being unavailable, which tells CI to retry: %v", err)
	}

	if !errors.Is(err, errs.ErrMissingInput) {
		t.Errorf("CopyTag missing-source error = %v, want ErrMissingInput", err)
	}
}

// TestIsLoopbackHost_SplitsHostAndPortLikeNet pins the loopback predicate on
// the shapes a registry host takes: the IPv6 loopback used to be unreachable
// because a LastIndex(":") split cut "::1" into "":"1".
func TestIsLoopbackHost_SplitsHostAndPortLikeNet(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		host string
		want bool
	}{
		{loopbackName, true},
		{loopbackName + ":5000", true},
		{"127.0.0.1:5000", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"[::1]:5000", true},
		{"ghcr.io", false},
		{"registry.example:443", false},
		// The name is matched whole: a loopback ref goes over plain HTTP.
		{loopbackName + ".example", false},
		{loopbackName + "x:5000", false},
		{"10.0.0.1:5000", false},
	} {
		if got := isLoopbackHost(tc.host); got != tc.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// credentialedRegistry starts an in-process registry that serves only the
// Basic credential ci:secret, holding owner/app:v1, and reports the
// Authorization headers it was sent. The developer's own registry credentials
// are out of reach: the ambient Docker configuration is the returned empty
// directory.
func credentialedRegistry(t *testing.T) (string, string, func() []string) {
	t.Helper()

	ambient := t.TempDir()
	for _, variable := range []string{"HOME", "DOCKER_CONFIG", "XDG_RUNTIME_DIR"} {
		t.Setenv(variable, ambient)
	}

	t.Setenv("REGISTRY_AUTH_FILE", "")

	var (
		mu   sync.Mutex
		sent []string
	)

	inner := registry.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if header := r.Header.Get("Authorization"); header != "" {
			mu.Lock()

			sent = append(sent, header)

			mu.Unlock()
		}

		if user, password, ok := r.BasicAuth(); !ok || user != "ci" || password != "secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)

			return
		}

		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	host := strings.TrimPrefix(srv.URL, "http://")

	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}

	if err := crane.Push(img, host+"/owner/app:v1", crane.Insecure, crane.WithAuth(&authn.Basic{Username: "ci", Password: "secret"})); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	sent = nil
	mu.Unlock()

	return host, ambient, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return slices.Clone(sent)
	}
}

func writeRegistryAuth(t *testing.T, directory, host string) string {
	t.Helper()

	document := `{"auths":{}}`
	if host != "" {
		document = fmt.Sprintf(`{"auths":{%q:{"username":"ci","password":"secret"}}}`, host)
	}

	path := filepath.Join(directory, "config.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// The auth file is where the credential comes from: with nothing ambient, a
// registry that demands one is still reached.
func TestAuthFile_SuppliesTheRegistryCredential(t *testing.T) { //nolint:paralleltest // the fixture sets process environment.
	host, _, _ := credentialedRegistry(t)

	tags, err := WithAuthFile(writeRegistryAuth(t, t.TempDir(), host)).ListContainerPackageVersions(t.Context(), host+"/owner", "app")
	if err != nil || !slices.Equal(tags, []string{"v1"}) {
		t.Fatalf("tags = %v, err = %v; the auth file's credential was not used", tags, err)
	}
}

// An auth file without an entry for the registry means anonymous, never the
// ambient credential: keeping a job's other logins away from this registry is
// what naming an auth file is for.
func TestAuthFile_WithoutAnEntryStaysAnonymous(t *testing.T) { //nolint:paralleltest // the fixture sets process environment.
	host, ambient, sent := credentialedRegistry(t)
	writeRegistryAuth(t, ambient, host)

	_, err := WithAuthFile(writeRegistryAuth(t, t.TempDir(), "")).ListContainerPackageVersions(t.Context(), host+"/owner", "app")
	if !errors.Is(err, errs.ErrPermissionDenied) || len(sent()) != 0 {
		t.Fatalf("err = %v, credentials sent = %d; the ambient credential reached the registry", err, len(sent()))
	}

	// The ambient credential is real: without an auth file it is what is used.
	if tags, err := New().ListContainerPackageVersions(t.Context(), host+"/owner", "app"); err != nil || len(tags) != 1 {
		t.Fatalf("fixture: the ambient credential does not work: %v, %v", tags, err)
	}
}

// A public availability probe must not borrow a job's existing registry login.
func TestAnonymous_DoesNotUseAnAmbientCredential(t *testing.T) { //nolint:paralleltest // the fixture sets process environment.
	host, ambient, sent := credentialedRegistry(t)
	writeRegistryAuth(t, ambient, host)

	_, err := NewAnonymous().ListContainerPackageVersions(t.Context(), host+"/owner", "app")
	if !errors.Is(err, errs.ErrPermissionDenied) || len(sent()) != 0 {
		t.Fatalf("err = %v, credentials sent = %d; anonymous probe borrowed a registry login", err, len(sent()))
	}

	if tags, err := New().ListContainerPackageVersions(t.Context(), host+"/owner", "app"); err != nil || len(tags) != 1 {
		t.Fatalf("fixture: the ambient credential does not work: %v, %v", tags, err)
	}
}
