// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

type fakeSignerImageTool struct {
	// events records every call in order; failOn names the operation that
	// returns errSignerStep, and raw overrides the manifest body.
	events []string
	failOn string

	buildReqs []domaincontainer.SignerImageBuildToolRequest
	pushes    []string
	pushFails int
	raw       []byte
	layouts   []string
	digest    string

	mergeImage   string
	mergeDigests []string
	mergeTags    []string
	mergeFails   int
	platformOS   string
	platformArch string
}

var errSignerStep = errors.New("signer tool step failed")

func (f *fakeSignerImageTool) BuildSignerImage(_ context.Context, req domaincontainer.SignerImageBuildToolRequest, _ io.Writer) error {
	f.buildReqs = append(f.buildReqs, req)

	return f.step("build")
}

func (f *fakeSignerImageTool) ExportLocalImageToLayout(_ context.Context, image string, directory string, _ io.Writer) error {
	f.layouts = append(f.layouts, directory)
	if len(f.buildReqs) != 1 || image != f.buildReqs[0].LocalImage {
		return errSignerStep
	}

	return f.step("export")
}

func (f *fakeSignerImageTool) PushLayout(_ context.Context, authFile, directory, dest string) (string, error) {
	f.pushes = append(f.pushes, dest)
	if len(f.buildReqs) != 1 || authFile != f.buildReqs[0].AuthFile || len(f.layouts) != 1 || directory != f.layouts[0] {
		return "", errSignerStep
	}

	if err := f.step("push"); err != nil {
		return "", err
	}

	if f.pushFails > 0 {
		f.pushFails--

		return "", errors.New("temporary push failure") //nolint:err113 // test double error.
	}

	if f.digest != "" {
		return f.digest, nil
	}

	return manifestTestDigest(f.manifest(dest)), nil
}

func (f *fakeSignerImageTool) RawManifest(_ context.Context, authFile, image string, _ io.Writer) ([]byte, error) {
	if len(f.buildReqs) != 0 && authFile != f.buildReqs[0].AuthFile {
		return nil, errSignerStep
	}

	if err := f.step("raw"); err != nil {
		return nil, err
	}

	return f.manifest(image), nil
}

func (f *fakeSignerImageTool) Manifest(ctx context.Context, image string) ([]byte, error) {
	return f.RawManifest(ctx, "", image, io.Discard)
}

func (f *fakeSignerImageTool) ImagePlatform(_ context.Context, ref string) (string, string, error) {
	if err := f.step("platform"); err != nil {
		return "", "", err
	}

	if f.platformOS != "" || f.platformArch != "" {
		return f.platformOS, f.platformArch, nil
	}

	if strings.HasSuffix(ref, strings.Repeat("2", 64)) {
		return "linux", "arm64", nil
	}

	return "linux", "amd64", nil
}

func (f *fakeSignerImageTool) MergeManifestWithDigest(_ context.Context, image string, digests, tags []string) (string, error) {
	f.mergeImage, f.mergeDigests, f.mergeTags = image, slices.Clone(digests), slices.Clone(tags)
	if err := f.step("merge"); err != nil {
		return "", err
	}

	if f.mergeFails > 0 {
		f.mergeFails--

		return "", errSignerStep
	}

	if f.digest != "" {
		return f.digest, nil
	}

	return manifestTestDigest(f.manifest(tags[0])), nil
}

func (f *fakeSignerImageTool) manifest(image string) []byte {
	if f.raw != nil {
		return f.raw
	}

	return []byte(`{"schemaVersion":2,"annotations":{"image":"` + image + `"}}`)
}

func (f *fakeSignerImageTool) step(name string) error {
	f.events = append(f.events, name)
	if f.failOn == name {
		return errSignerStep
	}

	return nil
}

func TestSignerImageRejectsSourceThatSuffixWouldRepair(t *testing.T) {
	for _, tc := range []struct{ repository, suffix string }{
		{"Org/", "image"}, {"Org", "/image"},
	} {
		for _, phase := range []string{"build", "assemble"} {
			t.Run(tc.repository+"/"+phase, func(t *testing.T) {
				t.Chdir(t.TempDir())
				writeAuthFileForSignerImage(t, "auth.json")

				if err := os.Mkdir("metadata", 0o700); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile("metadata/canary", []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}

				tool := &fakeSignerImageTool{failOn: "build"}

				var err error

				if phase == "build" {
					_, err = appcontainer.BuildSignerImageArch(t.Context(), tool, io.Discard, appcontainer.SignerImageBuildArchInput{
						AuthFile: "auth.json", Arch: "amd64", HostArch: "amd64", SourceSHA: strings.Repeat("a", 40),
						ServerURL: "https://forge.example", Repository: tc.repository, RepositorySuffix: tc.suffix, MetadataDir: "metadata",
					})
				} else {
					_, err = appcontainer.AssembleSignerImageManifest(t.Context(), tool, nil, nil, io.Discard, appcontainer.SignerImageAssembleInput{
						AuthFile: "auth.json", SourceSHA: strings.Repeat("a", 40),
						ServerURL: "https://forge.example", Repository: tc.repository, RepositorySuffix: tc.suffix, MetadataDir: "metadata",
					})
				}

				if !errors.Is(err, errs.ErrUsage) || len(tool.events) != 0 {
					t.Errorf("source not refused before tools: err=%v events=%v", err, tool.events)
				}

				body, readErr := os.ReadFile("metadata/canary")
				if readErr != nil || string(body) != "keep" {
					t.Errorf("invalid source changed metadata: %q, %v", body, readErr)
				}
			})
		}
	}
}

func TestSignerImageExternalDestinationPreservesSourceIdentity(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAuthFileForSignerImage(t, "auth.json")

	tool := &fakeSignerImageTool{}
	sourceSHA := strings.Repeat("a", 40)

	meta, err := appcontainer.BuildSignerImageArch(t.Context(), tool, io.Discard, appcontainer.SignerImageBuildArchInput{
		AuthFile: "auth.json", Arch: "amd64", HostArch: "amd64", SourceSHA: sourceSHA,
		ServerURL: "https://source.example", Repository: "Org/Engine",
		RepositorySuffix: "-signer", ImageRepository: "registry.example/policy/signer",
	})
	if err != nil {
		t.Fatal(err)
	}

	if meta.Tag != "registry.example/policy/signer:"+sourceSHA+"-amd64" ||
		meta.Ref != "registry.example/policy/signer@"+meta.Digest {
		t.Fatalf("external destination not used: %+v", meta)
	}

	if len(tool.buildReqs) != 1 || tool.buildReqs[0].SourceURL != "https://source.example/Org/Engine" ||
		tool.buildReqs[0].Revision != sourceSHA {
		t.Fatalf("external registry changed source identity: %+v", tool.buildReqs)
	}
}

func TestSignerImageRejectsInvalidExternalDestinationBeforeEffects(t *testing.T) {
	for _, repository := range []string{"https://registry.example/team/image", "registry.example/team/image:tag", "registry.example/team/image@sha256:" + strings.Repeat("a", 64), "registry.example/Team/image", "image", " registry.example/team/image"} {
		t.Run(repository, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeAuthFileForSignerImage(t, "auth.json")

			tool := &fakeSignerImageTool{}

			_, err := appcontainer.BuildSignerImageArch(t.Context(), tool, io.Discard, appcontainer.SignerImageBuildArchInput{
				AuthFile: "auth.json", Arch: "amd64", HostArch: "amd64", SourceSHA: strings.Repeat("a", 40),
				ServerURL: "https://source.example", Repository: "org/engine", ImageRepository: repository,
			})
			if !errors.Is(err, errs.ErrUsage) || len(tool.events) != 0 {
				t.Fatalf("invalid destination reached build: err=%v events=%v", err, tool.events)
			}

			_, err = appcontainer.AssembleSignerImageManifest(t.Context(), tool, nil, nil, io.Discard, appcontainer.SignerImageAssembleInput{
				AuthFile: "auth.json", SourceSHA: strings.Repeat("a", 40),
				ServerURL: "https://source.example", Repository: "org/engine", ImageRepository: repository,
			})
			if !errors.Is(err, errs.ErrUsage) || len(tool.events) != 0 {
				t.Fatalf("invalid destination reached assembly: err=%v events=%v", err, tool.events)
			}

			if _, err := os.Stat("image-arch-amd64"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid destination changed metadata: %v", err)
			}
		})
	}
}

// TestBuildSignerImageArch_NamesTheImageByTheDigestItPushed covers one
// architecture of the signer image: it is built, pushed with a retry, and
// described by metadata that is both returned and written to disk.
//
// The digest is asserted against a value computed outside this package. It was
// only ever checked against itself -- ref was compared to repo + "@" + digest,
// which holds just as well when the digest is empty -- and the digest is what
// identifies the image everything downstream signs and verifies.
func TestBuildSignerImageArch_NamesTheImageByTheDigestItPushed(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAuthFileForSignerImage(t, "auth.json")

	// The digest is the SHA-256 of the raw manifest the registry returns.
	const (
		rawManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
		wantDigest  = "sha256:b22c7289dd3b4785a3795c90e15d16bd66bd29b444b8974fe29ed0443ce50405"
		wantRepo    = "codeberg.org/itiquette/forgejo-ci-signer"
		sourceSHA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		recipeSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	tool := &fakeSignerImageTool{pushFails: 1, raw: []byte(rawManifest)}

	var out bytes.Buffer

	meta, err := appcontainer.BuildSignerImageArch(context.Background(), tool, &out, appcontainer.SignerImageBuildArchInput{
		AuthFile:         "auth.json",
		Arch:             "amd64",
		HostArch:         "amd64",
		SourceSHA:        sourceSHA,
		ServerURL:        "https://codeberg.org",
		Repository:       "Itiquette/Forgejo-CI",
		RepositorySuffix: "-signer",
		TagPrefix:        "signer-" + recipeSHA + "-",
		Name:             "signer-image",
		Title:            "forgejo-ci signer",
		RetryAttempts:    2,
		RetryDelay:       time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantTag := wantRepo + ":signer-" + recipeSHA + "-" + sourceSHA + "-amd64"
	if meta.Tag != wantTag {
		t.Errorf("tag = %q, want %q", meta.Tag, wantTag)
	}

	if meta.Digest != wantDigest {
		t.Errorf("digest = %q, want the manifest's SHA-256 %q", meta.Digest, wantDigest)
	}

	if want := wantRepo + "@" + wantDigest; meta.Ref != want {
		t.Errorf("ref = %q, want %q", meta.Ref, want)
	}

	// The retry pushes the same tag again rather than moving on.
	if !slices.Equal(tool.pushes, []string{wantTag, wantTag}) {
		t.Errorf("pushes = %v, want %q twice", tool.pushes, wantTag)
	}

	if len(tool.layouts) != 1 {
		t.Fatalf("exported %d layouts, want one despite the push retry", len(tool.layouts))
	}

	assertSignerLayoutsRemoved(t, tool.layouts)

	if !strings.Contains(out.String(), "attempt 1/2") {
		t.Errorf("retry message missing from output: %q", out.String())
	}

	if len(tool.buildReqs) != 1 {
		t.Fatalf("build requests = %+v, want exactly one", tool.buildReqs)
	}

	build := tool.buildReqs[0]
	if build.Revision != sourceSHA {
		t.Errorf("OCI revision = %q, want engine source %q, not recipe %q", build.Revision, sourceSHA, recipeSHA)
	}

	if build.Platform != "linux/amd64" {
		t.Errorf("built platform = %q, want linux/amd64", build.Platform)
	}

	if build.SourceURL != "https://codeberg.org/Itiquette/Forgejo-CI" {
		t.Errorf("source URL = %q", build.SourceURL)
	}

	if build.Title != "forgejo-ci signer" {
		t.Errorf("title = %q", build.Title)
	}

	// What is written to disk is what the next job reads; the returned value
	// never leaves this process.
	var disk appcontainer.SignerImageArchMetadata
	readJSONForSignerImage(t, "signer-image-arch-amd64/signer-image-amd64.json", &disk)

	if disk != *meta {
		t.Errorf("disk metadata = %+v, want %+v", disk, *meta)
	}
}

// TestAssembleSignerImageManifest_IndexesTheArchImagesItWasGiven covers the
// second half of the signer image build: the per-architecture images become one
// manifest index, pushed under the release tag and described by metadata.
//
// Like the per-arch half in 6c389caf, the digest is pinned to a value computed
// outside this package. The outputs were compared against the returned struct
// rather than to anything known, so an empty digest satisfied both sides.
func TestAssembleSignerImageManifest_IndexesTheArchImagesItWasGiven(t *testing.T) {
	t.Chdir(t.TempDir())

	const (
		repo        = "codeberg.org/itiquette/forgejo-ci-signer"
		sourceSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		rawManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json"}`
		wantDigest  = "sha256:d3e326ca75320da61d7a0abbbb4c24ac2719a2f2fea6029d645ef7a607027c58"
	)

	amd64Digest := strings.Repeat("1", 64)
	arm64Digest := strings.Repeat("2", 64)

	writeAuthFileForSignerImage(t, "auth.json")
	writeSignerArchMetadata(t, "amd64", repo, amd64Digest)
	writeSignerArchMetadata(t, "arm64", repo, arm64Digest)

	tool := &fakeSignerImageTool{raw: []byte(rawManifest), mergeFails: 1}
	sink := fakeoutputsink.New(t)

	meta, err := appcontainer.AssembleSignerImageManifest(t.Context(), tool, sink, nil, io.Discard, appcontainer.SignerImageAssembleInput{
		AuthFile:         "auth.json",
		SourceSHA:        sourceSHA,
		ServerURL:        "https://codeberg.org",
		Repository:       "itiquette/forgejo-ci",
		RepositorySuffix: "-signer",
		TagPrefix:        "signer-",
		Name:             "signer-image",
		RetryAttempts:    2,
		RetryDelay:       time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantTag := repo + ":signer-" + sourceSHA
	if meta.Tag != wantTag {
		t.Errorf("tag = %q, want %q", meta.Tag, wantTag)
	}

	if meta.Digest != wantDigest {
		t.Errorf("digest = %q, want the index manifest's SHA-256 %q", meta.Digest, wantDigest)
	}

	if want := repo + "@" + wantDigest; meta.Ref != want {
		t.Errorf("ref = %q, want %q", meta.Ref, want)
	}

	// The registry receives exactly the admitted children and final tag. Retrying
	// the merge cannot append to a stale local Buildah manifest.
	if tool.mergeImage != repo || !slices.Equal(tool.mergeDigests, []string{amd64Digest, arm64Digest}) ||
		!slices.Equal(tool.mergeTags, []string{wantTag}) {
		t.Errorf("merge = %s %v %v", tool.mergeImage, tool.mergeDigests, tool.mergeTags)
	}

	if !slices.Equal(tool.events, []string{"platform", "platform", "merge", "merge", "raw"}) || len(tool.buildReqs) != 0 || len(tool.pushes) != 0 {
		t.Errorf("assembly did not stay registry-only across retry: %v", tool.events)
	}

	for key, want := range map[string]string{
		"image-ref":    repo + "@" + wantDigest,
		"image-digest": wantDigest,
		"image-tag":    wantTag,
	} {
		if got := sink.Single(key); got != want {
			t.Errorf("output %s = %q, want %q", key, got, want)
		}
	}

	// What the next job reads is the file, not the returned struct.
	var disk appcontainer.SignerImageMetadata
	readJSONForSignerImage(t, "signer-image-dist/signer-image.json", &disk)

	if disk != *meta {
		t.Errorf("disk metadata = %+v, want %+v", disk, *meta)
	}
}

func TestAssembleSignerImageManifest_RejectsWrongRepositoryRef(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAuthFileForSignerImage(t, "auth.json")
	writeSignerArchMetadata(t, "amd64", "evil.example/itiquette/forgejo-ci-signer", strings.Repeat("1", 64))

	_, err := appcontainer.AssembleSignerImageManifest(context.Background(), &fakeSignerImageTool{}, fakeoutputsink.New(t), nil, io.Discard, appcontainer.SignerImageAssembleInput{
		AuthFile:         "auth.json",
		SourceSHA:        strings.Repeat("b", 40),
		ServerURL:        "https://codeberg.org",
		Repository:       "itiquette/forgejo-ci",
		RepositorySuffix: "-signer",
		TagPrefix:        "signer-",
		Name:             "signer-image",
		Archs:            []string{"amd64"},
	})
	if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "must be under codeberg.org/itiquette/forgejo-ci-signer") {
		t.Fatalf("err = %v", err)
	}
}

func TestAssembleSignerImageManifest_RejectsWrongPlatformBeforePublication(t *testing.T) {
	for _, platform := range [][2]string{{"windows", "amd64"}, {"linux", "arm64"}} {
		t.Run(platform[0]+"/"+platform[1], func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeAuthFileForSignerImage(t, "auth.json")
			writeSignerArchMetadata(t, "amd64", "codeberg.org/itiquette/forgejo-ci-signer", strings.Repeat("1", 64))

			if err := os.Mkdir("metadata", 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile("metadata/canary", []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}

			tool := &fakeSignerImageTool{platformOS: platform[0], platformArch: platform[1]}

			_, err := appcontainer.AssembleSignerImageManifest(t.Context(), tool, nil, nil, io.Discard, appcontainer.SignerImageAssembleInput{
				AuthFile: "auth.json", SourceSHA: strings.Repeat("b", 40), ServerURL: "https://codeberg.org",
				Repository: "itiquette/forgejo-ci", RepositorySuffix: "-signer", TagPrefix: "signer-", Name: "signer-image",
				Archs: []string{"amd64"}, MetadataDir: "metadata",
			})
			if !errors.Is(err, errs.ErrValidation) || !slices.Equal(tool.events, []string{"platform"}) {
				t.Fatalf("platform mismatch reached publication: events=%v err=%v", tool.events, err)
			}

			body, err := os.ReadFile("metadata/canary")
			if err != nil || string(body) != "keep" {
				t.Fatalf("platform mismatch changed existing metadata: %q %v", body, err)
			}
		})
	}
}

func writeAuthFileForSignerImage(t *testing.T, path string) { //nolint:unparam // explicit fixture path keeps call sites tied to the auth input under test.
	t.Helper()

	if err := os.WriteFile(path, []byte(`{"auths":{}}`), 0o600); err != nil { //nolint:gosec,mnd // test fixture.
		t.Fatal(err)
	}
}

func writeSignerArchMetadata(t *testing.T, arch, repo, digestHex string) {
	t.Helper()

	dir := "signer-image-arch-" + arch
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec,mnd // test fixture.
		t.Fatal(err)
	}

	body, err := json.Marshal(appcontainer.SignerImageArchMetadata{
		Arch:   arch,
		Tag:    repo + ":signer-" + strings.Repeat("b", 40) + "-" + arch,
		Digest: "sha256:" + digestHex,
		Ref:    repo + "@sha256:" + digestHex,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "signer-image-"+arch+".json"), body, 0o644); err != nil { //nolint:gosec,mnd // test fixture.
		t.Fatal(err)
	}
}

func readJSONForSignerImage(t *testing.T, path string, out any) {
	t.Helper()

	body, err := os.ReadFile(path) //nolint:gosec // test fixture.
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(body, out); err != nil {
		t.Fatal(err)
	}
}

// TestBuildSignerImageArch_HandsTheBuilderTheWholeRequest compares the build
// request whole against one written out here, with every optional input set
// to a non-default value. The existing test read three fields, so the auth
// file, revision, local image, Containerfile and context could be dropped.
func TestBuildSignerImageArch_HandsTheBuilderTheWholeRequest(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAuthFileForSignerImage(t, "auth.json")

	sourceSHA := strings.Repeat("c", 40)
	tool := &fakeSignerImageTool{}

	if _, err := appcontainer.BuildSignerImageArch(context.Background(), tool, io.Discard, appcontainer.SignerImageBuildArchInput{
		AuthFile: "auth.json", Arch: "arm64", HostArch: "arm64", SourceSHA: sourceSHA, ServerURL: "https://codeberg.org/", Repository: "Itiquette/Forgejo-CI",
		RepositorySuffix: "-signer", TagPrefix: "signer-", Name: "signer-image", Title: "forgejo-ci signer",
		Containerfile: "build/Signer.Containerfile", Context: "build", MetadataDir: "out/meta", RetryAttempts: 1,
	}); err != nil {
		t.Fatal(err)
	}

	want := []domaincontainer.SignerImageBuildToolRequest{{
		AuthFile: "auth.json", Platform: "linux/arm64", SourceURL: "https://codeberg.org/Itiquette/Forgejo-CI", Revision: sourceSHA,
		LocalImage: "localhost/signer-image:" + sourceSHA + "-arm64", Containerfile: "build/Signer.Containerfile", Context: "build",
		Title: "forgejo-ci signer",
	}}
	if !slices.Equal(tool.buildReqs, want) {
		t.Errorf("build requests =\n%+v\nwant\n%+v", tool.buildReqs, want)
	}

	if _, err := os.Stat("out/meta/signer-image-arm64.json"); err != nil {
		t.Errorf("metadata not written under the requested directory: %v", err)
	}
}

// A foreign architecture would build under CPU emulation. The refusal comes
// before any build, metadata or staging effect, and names the remedy.
func TestBuildSignerImageArch_RefusesAForeignHostArchitecture(t *testing.T) {
	for _, host := range []string{"amd64", ""} {
		t.Run("host="+host, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeAuthFileForSignerImage(t, "auth.json")

			tool := &fakeSignerImageTool{}

			_, err := appcontainer.BuildSignerImageArch(context.Background(), tool, io.Discard, appcontainer.SignerImageBuildArchInput{
				AuthFile: "auth.json", Arch: "arm64", HostArch: host, SourceSHA: strings.Repeat("d", 40),
				ServerURL: "https://registry.example", Repository: "owner/app", MetadataDir: "meta",
			})
			if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "native arm64 host") ||
				!strings.Contains(err.Error(), "runner of that architecture") {
				t.Fatalf("err = %v, want a validation refusal naming the native-host remedy", err)
			}

			if len(tool.buildReqs) != 0 {
				t.Errorf("build requested despite refusal: %+v", tool.buildReqs)
			}

			if _, statErr := os.Stat("meta"); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("metadata directory created despite refusal: %v", statErr)
			}
		})
	}
}

// TestSignerImage_EachFailureStopsEverythingAfterIt fails each operation of
// both phases in turn, and a registry answer that is not a manifest. Nothing
// after the failing step runs, no metadata file is written, and no output or
// summary is published -- the next job reads the metadata file, so a partial
// one would be taken as a finished image.
func TestSignerImage_EachFailureStopsEverythingAfterIt(t *testing.T) {
	sourceSHA := strings.Repeat("b", 40)
	repo := "codeberg.org/itiquette/forgejo-ci-signer"

	archInput := appcontainer.SignerImageBuildArchInput{
		AuthFile: "auth.json", Arch: "amd64", HostArch: "amd64", SourceSHA: sourceSHA, ServerURL: "https://codeberg.org", Repository: "itiquette/forgejo-ci",
		RepositorySuffix: "-signer", TagPrefix: "signer-", Name: "signer-image", RetryAttempts: 1,
	}

	for name, tc := range map[string]struct {
		failOn     string
		raw        []byte
		digest     string
		wantEvents []string
		wantErr    error
	}{
		"build":         {failOn: "build", wantEvents: []string{"build"}, wantErr: errSignerStep},
		"export":        {failOn: "export", wantEvents: []string{"build", "export"}, wantErr: errSignerStep},
		"push":          {failOn: "push", wantEvents: []string{"build", "export", "push"}, wantErr: errSignerStep},
		"raw manifest":  {failOn: "raw", wantEvents: []string{"build", "export", "push", "raw"}, wantErr: errSignerStep},
		"empty body":    {raw: []byte{}, wantEvents: []string{"build", "export", "push", "raw"}, wantErr: errs.ErrMalformedInput},
		"json null":     {raw: []byte("null"), wantEvents: []string{"build", "export", "push", "raw"}, wantErr: errs.ErrMalformedInput},
		"not an object": {raw: []byte(`["manifest"]`), wantEvents: []string{"build", "export", "push", "raw"}, wantErr: errs.ErrMalformedInput},
		"wrong digest":  {digest: "sha256:" + strings.Repeat("0", 64), wantEvents: []string{"build", "export", "push", "raw"}, wantErr: errs.ErrValidation},
	} {
		t.Run("arch "+name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeAuthFileForSignerImage(t, "auth.json")

			tool := &fakeSignerImageTool{failOn: tc.failOn, raw: tc.raw, digest: tc.digest}

			meta, err := appcontainer.BuildSignerImageArch(context.Background(), tool, io.Discard, archInput)
			if !errors.Is(err, tc.wantErr) || meta != nil {
				t.Errorf("meta = %+v, err = %v; want %v", meta, err, tc.wantErr)
			}

			if !slices.Equal(tool.events, tc.wantEvents) {
				t.Errorf("events = %q, want %q", tool.events, tc.wantEvents)
			}

			if entries, readErr := os.ReadDir("signer-image-arch-amd64"); readErr != nil || len(entries) != 0 {
				t.Errorf("metadata dir = %v (err %v), want an empty directory", entries, readErr)
			}

			assertSignerLayoutsRemoved(t, tool.layouts)
		})
	}

	assembleInput := appcontainer.SignerImageAssembleInput{
		AuthFile: "auth.json", SourceSHA: sourceSHA, ServerURL: "https://codeberg.org", Repository: "itiquette/forgejo-ci",
		RepositorySuffix: "-signer", TagPrefix: "signer-", Name: "signer-image", RetryAttempts: 1,
	}
	all := []string{"platform", "platform", "merge", "raw"}

	for name, tc := range map[string]struct {
		failOn     string
		failKey    string
		digest     string
		summaryErr error
		wantEvents []string
		wantErr    error
		wantFile   bool
		wantKeys   []string
	}{
		"platform":         {failOn: "platform", wantEvents: all[:1], wantErr: errSignerStep},
		"merge":            {failOn: "merge", wantEvents: all[:3], wantErr: errSignerStep},
		"raw manifest":     {failOn: "raw", wantEvents: all, wantErr: errSignerStep},
		"wrong digest":     {digest: "sha256:" + strings.Repeat("0", 64), wantEvents: all, wantErr: errs.ErrValidation},
		"image-ref output": {failKey: "image-ref", wantEvents: all, wantErr: errSignerStep, wantFile: true},
		"image-tag output": {failKey: "image-tag", wantEvents: all, wantErr: errSignerStep, wantFile: true, wantKeys: []string{"image-ref", "image-digest"}},
		"summary":          {summaryErr: errSignerStep, wantEvents: all, wantErr: errSignerStep, wantFile: true, wantKeys: []string{"image-ref", "image-digest", "image-tag"}},
	} {
		t.Run("assemble "+name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeAuthFileForSignerImage(t, "auth.json")
			writeSignerArchMetadata(t, "amd64", repo, strings.Repeat("1", 64))
			writeSignerArchMetadata(t, "arm64", repo, strings.Repeat("2", 64))

			tool := &fakeSignerImageTool{failOn: tc.failOn, digest: tc.digest}
			sink := &signerFailingSink{Sink: fakeoutputsink.New(t), failKey: tc.failKey}
			summary := &signerSummary{err: tc.summaryErr}

			var out bytes.Buffer

			meta, err := appcontainer.AssembleSignerImageManifest(context.Background(), tool, sink, summary, &out, assembleInput)
			if !errors.Is(err, tc.wantErr) || meta != nil {
				t.Errorf("meta = %+v, err = %v; want %v", meta, err, tc.wantErr)
			}

			if !slices.Equal(tool.events, tc.wantEvents) {
				t.Errorf("events = %q, want %q", tool.events, tc.wantEvents)
			}

			if _, statErr := os.Stat("signer-image-dist/signer-image.json"); (statErr == nil) != tc.wantFile {
				t.Errorf("metadata file present = %v, want %v", statErr == nil, tc.wantFile)
			}

			if got := sink.Order(); !slices.Equal(got, tc.wantKeys) {
				t.Errorf("outputs = %q, want %q", got, tc.wantKeys)
			}

			if strings.Contains(out.String(), "Image manifest pushed") {
				t.Errorf("stdout claims success: %q", out.String())
			}
		})
	}
}

func assertSignerLayoutsRemoved(t *testing.T, directories []string) {
	t.Helper()

	for _, directory := range directories {
		if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("publication retained its temporary layout: %v", err)
		}
	}
}

type signerFailingSink struct {
	*fakeoutputsink.Sink

	failKey string
}

func (s *signerFailingSink) Set(ctx context.Context, key, value string) error {
	if key == s.failKey {
		return errSignerStep
	}

	return s.Sink.Set(ctx, key, value)
}

type signerSummary struct {
	err     error
	appends int
}

func (s *signerSummary) Append(context.Context, string) error {
	s.appends++

	return s.err
}
