// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// The actual wallet-r2ps workspace: three runtime images and four binaries
// extracted from the scanned layouts, published unsigned without registry tags.
func TestGitLabLayer_RustDevelopment(t *testing.T) {
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("set FORGE_CI_LIVE_CONSUMER to the clean wallet-r2ps development fixture and FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE to RustBuild")
	}
	if livetest.DemoName() != "" {
		t.Fatal("Rust development requires disposable scratch repositories")
	}
	if !livetest.ImageBuilds(t) {
		t.Skip("this runner may not build images")
	}
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabDevelopment(t, root, visibility.Private, developmentAcceptance{
				jobName: "Check: Rust development", smokeMarker: "Rust development smoke passed at ",
				artifactPrefix: "fc-rust-dev-", maxArchive: 256 << 20,
				checkPublication: requireRustDevelopmentPublication,
			})
		})
	}
}

func requireRustDevelopmentPublication(t *testing.T, body []byte, commit string, target livetest.Target, repo string) {
	t.Helper()
	if livetest.RunnerArch(t, target) != "amd64" {
		t.Fatal("wallet-r2ps requires a native linux/amd64 runner")
	}
	files := rustDevelopmentArchive(t, body)
	var receipt struct {
		SourceSHA string            `json:"sourceSHA"`
		Version   string            `json:"version"`
		Files     map[string]string `json:"files"`
	}
	if err := json.Unmarshal(files["receipt.json"], &receipt); err != nil {
		t.Fatal(err)
	}
	version := "0.1.0-dev." + commit[:12]
	if receipt.SourceSHA != commit || receipt.Version != version || len(receipt.Files) != 7 {
		t.Fatalf("receipt is not bound to %s at %s", version, commit)
	}
	for name, data := range files {
		if name == "receipt.json" {
			continue
		}
		sum := sha256.Sum256(data)
		if receipt.Files[name] != hex.EncodeToString(sum[:]) {
			t.Fatalf("receipt hash mismatch for %s", name)
		}
	}
	for _, binary := range []string{"hsm-worker", "digg-hsm-keytool", "wallet-bff", "integration-load-tests"} {
		name := "linux-amd64/" + binary + "-linux-amd64"
		image, err := elf.NewFile(bytes.NewReader(files[name]))
		if err != nil {
			t.Fatalf("%s is not ELF: %v", name, err)
		}
		if image.Class != elf.ELFCLASS64 || image.Machine != elf.EM_X86_64 || (image.Type != elf.ET_EXEC && image.Type != elf.ET_DYN) {
			t.Fatalf("%s is not a linux/amd64 executable", name)
		}
		_ = image.Close()
	}
	host, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}
	imageRef := host + "/" + livetest.RepoSlug(target, repo)
	work := t.TempDir()
	auth := livetest.RegistryAuthFile(t, target, work)
	seen := map[string]bool{}
	for _, role := range []string{"hsm-worker", "wallet-bff", "integration-load-tests"} {
		var record appcontainer.ImageLayoutRecord
		if err := json.Unmarshal(files[role+"-image.json"], &record); err != nil {
			t.Fatal(err)
		}
		if record.SourceSHA != commit || record.Platform != "linux/amd64" || record.ImageRef != imageRef || !container.ValidDigest(record.Digest) || seen[record.Digest] {
			t.Fatalf("invalid or duplicate %s layout record: %+v", role, record)
		}
		seen[record.Digest] = true
		// ImageDigest uses name.NewTag, so a digest cannot be passed as its tag.
		// The existing usable-digest CLI uses the direct OCI registry adapter and
		// the live CLI harness supplies lab trust/network settings and credentials.
		readback := livetest.CLIIn(t, target, repo, livetest.RunOptions{Dir: work},
			"container", "image", "usable-digest", "--ref", imageRef+"@"+record.Digest,
			"--arch", "amd64", "--auth-file", auth,
			"--require-label", "org.opencontainers.image.revision="+commit,
			"--require-label", "org.opencontainers.image.version="+version)
		if readback.ExitCode != 0 || strings.TrimSpace(readback.Stdout) != record.Digest {
			t.Fatalf("%s registry readback failed: exit=%d stdout=%s stderr=%s", role, readback.ExitCode, readback.Stdout, readback.Stderr)
		}
		paths := map[string][]string{
			"hsm-worker":             {"hsm-worker", "usr/local/bin/digg-hsm-keytool"},
			"wallet-bff":             {"wallet-bff"},
			"integration-load-tests": {"usr/local/bin/integration-load-tests"},
		}[role]
		for imagePath, hash := range livetest.ImageFileSHA256(t, target, repo, record.Digest, paths) {
			binary := path.Base(imagePath)
			if receipt.Files["linux-amd64/"+binary+"-linux-amd64"] != hash {
				t.Fatalf("downloaded %s differs from published image %s", binary, record.Digest)
			}
		}
	}
	if tags := livetest.RegistrySnapshot(t, target, repo); len(tags) != 0 {
		t.Fatalf("tagless development created registry tags: %v", tags)
	}
	t.Logf("native receipt binds four ELF binaries and three registry runtime digests to %s (%s)", commit, version)
}

func rustDevelopmentArchive(t *testing.T, body []byte) map[string][]byte {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		"receipt.json":          16 << 10,
		"hsm-worker-image.json": 16 << 10, "wallet-bff-image.json": 16 << 10,
		"integration-load-tests-image.json":              16 << 10,
		"linux-amd64/hsm-worker-linux-amd64":             256 << 20,
		"linux-amd64/digg-hsm-keytool-linux-amd64":       256 << 20,
		"linux-amd64/wallet-bff-linux-amd64":             256 << 20,
		"linux-amd64/integration-load-tests-linux-amd64": 256 << 20,
	}
	files := map[string][]byte{}
	remaining := int64(256 << 20)
	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			continue
		}
		name := strings.TrimPrefix(file.Name, "development-artifacts/")
		limit, ok := want[name]
		if !strings.HasPrefix(file.Name, "development-artifacts/") || !ok || files[name] != nil || !file.Mode().IsRegular() {
			t.Fatalf("unexpected native artifact %s", file.Name)
		}
		limit = min(limit, remaining)
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(stream, limit+1))
		_ = stream.Close()
		if err != nil || len(data) == 0 || int64(len(data)) > limit {
			t.Fatalf("invalid or oversized artifact %s: %v", name, err)
		}
		remaining -= int64(len(data))
		files[name] = data
	}
	if len(files) != len(want) {
		t.Fatalf("want exactly eight native files, got %v", sortedNames(files))
	}
	return files
}
