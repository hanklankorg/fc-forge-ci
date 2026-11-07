// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/cosign"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	domainsbom "github.com/diggsweden/forge-ci/internal/domain/sbom"
)

type nativeReleaseFixture struct {
	producer, signer       map[string][]byte
	publisher              map[string][]byte
	published              map[string][]byte
	checksum, root, binary string
	adapter                *cosign.Adapter
}

func TestGitLabReleaseEvidenceNativeChain(t *testing.T) {
	binary, cosignPath := Binary(t), HostTool(t, "cosign")
	for _, goMetadata := range []bool{false, true} {
		t.Run(fmt.Sprintf("goreleaser=%t", goMetadata), func(t *testing.T) {
			fixture := newNativeReleaseFixture(t, binary, cosignPath, goMetadata)

			got, err := fixture.check(t, fixture.producer, fixture.signer, fixture.published)
			if err != nil || got.ChecksumName != fixture.checksum || got.Verified != 4 {
				t.Fatalf("native producer/signer/published chain: %+v, %v", got, err)
			}
		})
	}
}

func TestGitLabReleaseEvidenceRejectsSubstitutions(t *testing.T) {
	fixture := newNativeReleaseFixture(t, Binary(t), HostTool(t, "cosign"), true)
	for _, test := range []struct {
		name   string
		mutate func(map[string][]byte, map[string][]byte, map[string][]byte)
	}{
		{"published artifact wrong hash", func(_, _ map[string][]byte, p map[string][]byte) { p["app.tar.gz"] = []byte("substituted bytes") }},
		{"missing published artifact", func(_, _ map[string][]byte, p map[string][]byte) { delete(p, "app.tar.gz") }},
		{"listed artifact without bytes", func(_, _ map[string][]byte, p map[string][]byte) { p["app.tar.gz"] = nil }},
		{"extra published asset", func(_, _ map[string][]byte, p map[string][]byte) { p["undeclared.txt"] = []byte("extra") }},
		{"signed wrong checksum digest", func(_, s, p map[string][]byte) {
			wrong := bytes.Replace(s["dist/"+fixture.checksum], []byte(fmt.Sprintf("%x", sha256.Sum256(s["dist/app.tar.gz"]))), []byte(strings.Repeat("f", 64)), 1)
			s["dist/"+fixture.checksum], p[fixture.checksum] = wrong, wrong
			sig := fixture.sign(t, wrong)
			s["dist/"+fixture.checksum+".bundle"], p[fixture.checksum+".bundle"] = sig, sig
		}},
		{"signed missing checksum coverage", func(b, s, p map[string][]byte) {
			wrong := []byte("")
			b["dist/"+fixture.checksum], s["dist/"+fixture.checksum], p[fixture.checksum] = wrong, wrong, wrong
			sig := fixture.sign(t, wrong)
			s["dist/"+fixture.checksum+".bundle"], p[fixture.checksum+".bundle"] = sig, sig
		}},
		{"published checksum differs from retained signer", func(_, _ map[string][]byte, p map[string][]byte) {
			p["app.tar.gz"] = []byte("different but consistently checksummed")
			p[fixture.checksum] = bytes.Replace(p[fixture.checksum], []byte(fmt.Sprintf("%x", sha256.Sum256(fixture.published["app.tar.gz"]))), []byte(fmt.Sprintf("%x", sha256.Sum256(p["app.tar.gz"]))), 1)
			p[fixture.checksum+".bundle"] = fixture.sign(t, p[fixture.checksum])
		}},
		{"re-signed unrelated provenance", func(_, _ map[string][]byte, p map[string][]byte) {
			p[apprelease.ProvenanceStatementName] = []byte(`{"unrelated":"another invocation"}`)
			p[apprelease.ProvenanceStatementName+".bundle"] = fixture.sign(t, p[apprelease.ProvenanceStatementName])
		}},
		{"re-signed unrelated evidence", func(_, _ map[string][]byte, p map[string][]byte) {
			p["release-notes.md"] = []byte("different release notes")
			p["release-notes.md.bundle"] = fixture.sign(t, p["release-notes.md"])
		}},
		{"spoofed signer checksum declaration", func(_, s, _ map[string][]byte) {
			mutateNativeManifest(t, s, func(m *apprelease.FileManifest) { m.Checksums[0] = m.Provenance[0] })
		}},
		{"fabricated producer source", func(b, _, _ map[string][]byte) {
			mutateNativeManifest(t, b, func(m *apprelease.FileManifest) { m.Checksums[0].Source = "fabricated" })
		}},
		{"missing signer source", func(_, s, _ map[string][]byte) {
			mutateNativeManifest(t, s, func(m *apprelease.FileManifest) { m.Assets[0].Source = "" })
		}},
		{"duplicate section entry", func(_, s, _ map[string][]byte) {
			mutateNativeManifest(t, s, func(m *apprelease.FileManifest) { m.Checksums = append(m.Checksums, m.Checksums[0]) })
		}},
		{"conflicting section", func(_, s, _ map[string][]byte) {
			mutateNativeManifest(t, s, func(m *apprelease.FileManifest) { m.Evidence = append(m.Evidence, m.Checksums[0]) })
		}},
		{"absolute manifest path", func(_, s, _ map[string][]byte) {
			mutateNativeManifest(t, s, func(m *apprelease.FileManifest) { m.Checksums[0].Path = "/" + fixture.checksum })
		}},
		{"traversing manifest path", func(_, s, _ map[string][]byte) {
			mutateNativeManifest(t, s, func(m *apprelease.FileManifest) { m.Checksums[0].Path = "dist/../" + fixture.checksum })
		}},
		{"URL manifest path", func(_, s, _ map[string][]byte) {
			mutateNativeManifest(t, s, func(m *apprelease.FileManifest) { m.Checksums[0].Path = "https://example.invalid/" + fixture.checksum })
		}},
		{"malformed manifest field", func(_, s, _ map[string][]byte) { s["dist/release-files.json"] = []byte(`{"version":"one"}`) }},
		{"normalized path collision", func(_, s, _ map[string][]byte) {
			mutateNativeManifest(t, s, func(m *apprelease.FileManifest) {
				a := m.Assets[0]
				a.Path = "dist/./" + a.Name
				m.Assets = append(m.Assets, a)
			})
		}},
		{"unknown manifest field", func(_, s, _ map[string][]byte) {
			s["dist/release-files.json"] = bytes.Replace(s["dist/release-files.json"], []byte(`"version":`), []byte(`"unrecognized":true,"version":`), 1)
		}},
		{"missing checksum bundle", func(_, _ map[string][]byte, p map[string][]byte) { delete(p, fixture.checksum+".bundle") }},
		{"missing provenance bundle", func(_, _ map[string][]byte, p map[string][]byte) {
			delete(p, apprelease.ProvenanceStatementName+".bundle")
		}},
		{"corrupt Cosign bundle", func(_, _ map[string][]byte, p map[string][]byte) { p[fixture.checksum+".bundle"] = []byte("corrupt") }},
		{"mismatched Cosign bundle", func(_, _ map[string][]byte, p map[string][]byte) {
			p[fixture.checksum+".bundle"] = p[apprelease.ProvenanceStatementName+".bundle"]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, s, p := maps.Clone(fixture.producer), maps.Clone(fixture.signer), maps.Clone(fixture.published)
			test.mutate(b, s, p)

			if _, err := fixture.check(t, b, s, p); err == nil {
				t.Fatal("accepted substituted native release evidence")
			}
		})
	}

	t.Run("wrong trusted key", func(t *testing.T) {
		wrong := newNativeReleaseFixture(t, fixture.binary, fixture.adapter.Bin, false)

		original := fixture.root

		fixture.root = wrong.root
		defer func() { fixture.root = original }()

		if _, err := fixture.check(t, fixture.producer, fixture.signer, fixture.published); err == nil {
			t.Fatal("accepted bundle under wrong trusted key")
		}
	})
}

func newNativeReleaseFixture(t *testing.T, binary, cosignPath string, goMetadata bool) *nativeReleaseFixture {
	t.Helper()

	return newNativeReleaseJourney(t, binary, cosignPath, goMetadata, false)
}

func newNativeReleaseJourney(t *testing.T, binary, cosignPath string, goMetadata, images bool) *nativeReleaseFixture {
	t.Helper()
	root := t.TempDir()

	producer, signer := filepath.Join(root, "producer"), filepath.Join(root, "signer")
	if err := os.MkdirAll(filepath.Join(producer, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer

	gz := gzip.NewWriter(&archive)

	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "README", Mode: 0o600, Size: 7}); err != nil {
		t.Fatal(err)
	}

	if _, err := tw.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	write := func(name string, body []byte) {
		t.Helper()

		if err := os.WriteFile(name, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(producer, "dist/app.tar.gz"), archive.Bytes())

	sbom := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","version":1}`)
	write(filepath.Join(producer, "dist/app.tar.gz.sbom.json"), sbom)
	write(filepath.Join(producer, "dist/release-notes.md"), []byte("# Native fixture release\n"))

	checksum := "checksums.txt"
	if goMetadata {
		checksum = "gommitlint_0.94.0_checksums.txt"
		write(filepath.Join(producer, "dist/"+checksum), []byte(fmt.Sprintf("%x  app.tar.gz\n%x  app.tar.gz.sbom.json\n", sha256.Sum256(archive.Bytes()), sha256.Sum256(sbom))))
		write(filepath.Join(producer, "dist/artifacts.json"), []byte(`[{"path":"dist/app.tar.gz","type":"Archive"},{"path":"dist/app.tar.gz.sbom.json","type":"SBOM"},{"path":"dist/`+checksum+`","type":"Checksum"}]`))
		runNativeEvidenceCLI(t, binary, producer, "import-goreleaser", "--manifest", "dist/release-files.json", "--evidence-file", "dist/release-notes.md")
	} else {
		runNativeEvidenceCLI(t, binary, producer, "declare", "--asset", "dist/app.tar.gz", "--sbom", "dist/app.tar.gz.sbom.json", "--evidence-file", "dist/release-notes.md")
	}

	input := producer

	var (
		publisher  map[string][]byte
		imageNames []string
	)

	if images {
		input = filepath.Join(root, "publisher")
		if err := os.CopyFS(input, os.DirFS(producer)); err != nil {
			t.Fatal(err)
		}

		imageNames = addNativeImageEvidence(t, binary, input)
		publisher = nativeEvidenceTree(t, input)
	}

	if err := os.CopyFS(signer, os.DirFS(input)); err != nil {
		t.Fatal(err)
	}

	key := exec.CommandContext(t.Context(), cosignPath, "generate-key-pair", "--output-key-prefix", filepath.Join(root, "key"))

	key.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMPDIR=" + root, "COSIGN_PASSWORD=", "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9"}
	if out, err := key.CombinedOutput(); err != nil {
		t.Fatalf("offline ephemeral key generation: %v\n%s", err, out)
	}

	fixture := &nativeReleaseFixture{root: root, binary: binary, checksum: checksum, publisher: publisher, adapter: &cosign.Adapter{Bin: cosignPath, Env: key.Env, Transparency: domainrelease.TransparencyNone}}

	sum, err := os.ReadFile(filepath.Join(signer, "dist/"+checksum))
	if err != nil {
		t.Fatal(err)
	}

	prov, err := apprelease.GenerateProvenance(apprelease.ProvenanceInput{Checksums: bytes.NewReader(sum), RepositoryURL: "https://example.invalid/fixture/app", Ref: "v0.94.0", SHA: strings.Repeat("a", 64), BuilderID: "https://example.invalid/native/gitlab/release", InvocationID: "https://example.invalid/pipelines/471/jobs/1107", StartedOn: "2026-11-04T11:44:00Z"})
	if err != nil {
		t.Fatal(err)
	}

	write(filepath.Join(signer, "dist/"+apprelease.ProvenanceStatementName), prov)
	write(filepath.Join(signer, "dist/"+checksum+".bundle"), fixture.sign(t, sum))
	write(filepath.Join(signer, "dist/"+apprelease.ProvenanceStatementName+".bundle"), fixture.sign(t, prov))

	for _, name := range append([]string{"app.tar.gz.sbom.json", "release-notes.md"}, imageNames...) {
		body, err := os.ReadFile(filepath.Join(signer, "dist/"+name))
		if err != nil {
			t.Fatal(err)
		}

		write(filepath.Join(signer, "dist/"+name+".bundle"), fixture.sign(t, body))
	}

	runNativeEvidenceCLI(t, binary, signer, "manifest", "--manifest", "dist/release-files.json")
	fixture.producer, fixture.signer = nativeEvidenceTree(t, producer), nativeEvidenceTree(t, signer)

	m, err := readReleaseEvidenceManifest(signer, "dist/release-files.json")
	if err != nil {
		t.Fatal(err)
	}

	fixture.published = make(map[string][]byte)
	for _, a := range m.Assets {
		fixture.published[a.Name] = fixture.signer[a.Path]
	}

	return fixture
}

func addNativeImageEvidence(t *testing.T, binary, root string) []string {
	t.Helper()

	const repository = "registry.example/org/app"

	entries := make([]imageledger.Entry, 0, 2)
	names := make([]string, 0, 4)

	for _, role := range []string{"app", "distroless"} {
		child := "sha256:" + strings.Repeat("a", 64)
		index := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","size":123,"digest":"` + child + `","platform":{"os":"linux","architecture":"amd64"}}]}`)
		inventory := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","version":1,"components":[]}`)
		ref := fmt.Sprintf("%s@sha256:%x", repository, sha256.Sum256(index))
		inventoryName := "sbom-" + role + "-amd64.json"
		bomName := "image-sbom-" + role + ".cyclonedx.json"

		bom, err := domainsbom.ComposeIndexBOM(ref, index, []domainsbom.IndexBOMPart{{
			Platform: "linux/amd64", ImageRef: repository + "@" + child,
			URL:    "https://example.invalid/org/app/releases/download/v0.94.0/" + inventoryName,
			SHA256: fmt.Sprintf("%x", sha256.Sum256(inventory)), Document: inventory,
		}})
		if err != nil {
			t.Fatal(err)
		}

		for name, body := range map[string][]byte{inventoryName: inventory, bomName: bom} {
			if err := os.WriteFile(filepath.Join(root, "dist", name), body, 0o600); err != nil {
				t.Fatal(err)
			}
		}

		entries = append(entries, imageledger.Entry{Role: role, ImageKind: imageledger.ImageKindRelease, Ref: ref,
			Digest: "sha256:" + fmt.Sprintf("%x", sha256.Sum256(index)), CandidateTag: repository + ":staging-v0.94.0-" + role,
			FinalTag: repository + ":v0.94.0-" + role, SBOM: "dist/" + bomName, SBOMSHA256: fmt.Sprintf("%x", sha256.Sum256(bom))})
		names = append(names, bomName, inventoryName)
	}

	body, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "dist/release-images.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	args := make([]string, 0, 6+2*len(names))

	args = append(args, "manifest", "--unsigned", "--manifest", "dist/release-files.json", "--release-images-ledger", "dist/release-images.json")
	for _, name := range names {
		args = append(args, "--evidence-file", "dist/"+name)
	}

	runNativeEvidenceCLI(t, binary, root, args...)

	return names
}

func (f *nativeReleaseFixture) sign(t *testing.T, blob []byte) []byte {
	t.Helper()
	dir := t.TempDir()

	file, bundle := filepath.Join(dir, "blob"), filepath.Join(dir, "blob.bundle")
	if err := os.WriteFile(file, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := f.adapter.SignBlob(t.Context(), cosign.SignBlobInput{KeyRef: filepath.Join(f.root, "key.key"), Artifact: file, BundlePath: bundle}, io.Discard); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func (f *nativeReleaseFixture) check(t *testing.T, producer, signer, published map[string][]byte) (GitLabReleaseBundleEvidence, error) {
	t.Helper()

	return f.checkWithPublisher(t, producer, nil, signer, published)
}

func (f *nativeReleaseFixture) checkWithPublisher(t *testing.T, producer, publisher, signer, published map[string][]byte) (GitLabReleaseBundleEvidence, error) {
	t.Helper()

	var publisherZIP []byte
	if publisher != nil {
		publisherZIP = nativeEvidenceZIP(t, publisher, "_release-images/")
	}

	return VerifyGitLabReleaseEvidence(t.Context(), f.binary, nativeEvidenceZIP(t, producer, "_release-build/"), publisherZIP, nativeEvidenceZIP(t, signer, "_release-sign/"), slices.Collect(maps.Keys(published)), func(name string) ([]byte, error) {
		b, ok := published[name]
		if !ok {
			return nil, fs.ErrNotExist
		}

		return b, nil
	}, func(blob, bundle []byte) error {
		dir := t.TempDir()

		file, sig := filepath.Join(dir, "blob"), filepath.Join(dir, "blob.bundle")
		if err := os.WriteFile(file, blob, 0o600); err != nil {
			return err
		}

		if err := os.WriteFile(sig, bundle, 0o600); err != nil {
			return err
		}

		return f.adapter.VerifyBlob(t.Context(), cosign.VerifyBlobInput{KeyRef: filepath.Join(f.root, "key.pub"), Artifact: file, BundlePath: sig}, io.Discard)
	})
}

func runNativeEvidenceCLI(t *testing.T, binary, root string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, append([]string{"release", "files"}, args...)...)
	cmd.Dir = root

	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMPDIR=" + root}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native producer/finalizer %v: %v\n%s", args, err, out)
	}
}

func nativeEvidenceTree(t *testing.T, root string) map[string][]byte {
	t.Helper()

	tree := make(map[string][]byte)

	err := filepath.WalkDir(filepath.Join(root, "dist"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		tree[filepath.ToSlash(name)] = body

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return tree
}

func nativeEvidenceZIP(t *testing.T, tree map[string][]byte, prefix string) []byte {
	t.Helper()
	// The artifact envelope carries the same native cross-job receipt as the
	// real handoff. Mutations below get a fresh receipt so their rejection must
	// come from contract/subject validation rather than a stale envelope hash.
	root := t.TempDir()
	for name, body := range tree {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	digest, err := apprelease.DistDigestWithManifestRoot(filepath.Join(root, "dist"), ".")
	if err != nil {
		t.Fatal(err)
	}

	tree = maps.Clone(tree)
	tree["digest"] = []byte(digest + "\n")

	var body bytes.Buffer

	z := zip.NewWriter(&body)
	for _, name := range slices.Sorted(maps.Keys(tree)) {
		w, err := z.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write(tree[name]); err != nil {
			t.Fatal(err)
		}
	}

	if err := z.Close(); err != nil {
		t.Fatal(err)
	}

	return body.Bytes()
}

func mutateNativeManifest(t *testing.T, tree map[string][]byte, change func(*apprelease.FileManifest)) {
	t.Helper()

	var m apprelease.FileManifest
	if err := json.Unmarshal(tree["dist/release-files.json"], &m); err != nil {
		t.Fatal(err)
	}

	change(&m)

	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	tree["dist/release-files.json"] = body
}
