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
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// A Rust workspace with three runtime images and four executable roles,
// extracted from scanned layouts and published unsigned without registry tags.
// The repository-owned fixture uses a fixed layout, independent of its receipt.
func TestGitLabLayer_RustDevelopment(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("set FORGE_CI_LIVE_CONSUMER to the Rust development fixture and FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE to its build image archive")
	}

	if livetest.DemoName() != "" {
		t.Fatal("Rust development requires disposable scratch repositories")
	}

	runGitLabDevelopment(t, root, developmentAcceptance{
		jobName: "Check: Rust development", smokeMarker: "Rust development smoke passed at ",
		artifactPrefix: "fc-rust-dev-", maxArchive: 256 << 20, buildsImages: true,
		checkArchive: rustFixtureRoles().requireArchive, checkPublication: rustFixtureRoles().requireRegistryPublication,
	})
}

func TestForgejoLayer_RustDevelopment(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("set FORGE_CI_LIVE_CONSUMER to the Rust development fixture; Buildah uses CI_RUST_BUILD_IMAGE, Kaniko uses FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE with the Rust toolchain")
	}

	if livetest.DemoName() != "" {
		t.Fatal("Rust development requires disposable scratch repositories")
	}

	runForgejoConsumerDevelopment(t, developmentAcceptance{
		jobName: "Check: Rust development", smokeMarker: "Rust development smoke passed at ",
		imageVariable: "CI_RUST_BUILD_IMAGE", artifactPrefix: "fc-rust-dev-", maxArchive: 256 << 20, buildsImages: true,
		checkArchive: rustFixtureRoles().requireArchive, checkPublication: rustFixtureRoles().requireRegistryPublication,
	})
}

func (roles rustRoles) requireArchive(t *testing.T, body []byte, commit string) {
	t.Helper()

	files, err := roles.archive(body)
	if err != nil {
		t.Fatal(err)
	}

	if err := roles.validateArtifacts(files, commit); err != nil {
		t.Fatal(err)
	}
}

func (roles rustRoles) requireRegistryPublication(t *testing.T, body []byte, commit string, target livetest.Target, repo string) {
	t.Helper()

	if livetest.RunnerArch(t, target) != "amd64" {
		t.Fatal("Rust development fixture requires a native linux/amd64 runner")
	}

	files, err := roles.archive(body)
	if err != nil {
		t.Fatal(err)
	}

	roles.requireRegistry(t, files, commit, target, repo)
}

func (roles rustRoles) validateArtifacts(files map[string][]byte, commit string) error {
	var receipt struct {
		SourceSHA string            `json:"sourceSHA"`
		Version   string            `json:"version"`
		Files     map[string]string `json:"files"`
	}
	if err := json.Unmarshal(files["receipt.json"], &receipt); err != nil {
		return err
	}

	if len(commit) < 12 {
		return fmt.Errorf("invalid source commit: %w", errs.ErrValidation)
	}

	version := "0.1.0-dev." + commit[:12]
	if receipt.SourceSHA != commit || receipt.Version != version || len(receipt.Files) != 7 {
		return fmt.Errorf("receipt is not bound to %s at %s: %w", version, commit, errs.ErrValidation)
	}

	for name, data := range files {
		if name == "receipt.json" {
			continue
		}

		sum := sha256.Sum256(data)
		if receipt.Files[name] != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("receipt hash mismatch for %s: %w", name, errs.ErrValidation)
		}
	}

	for _, binary := range []string{roles.Worker, roles.Tool, roles.Service, roles.LoadGenerator} {
		name := "linux-amd64/" + binary + "-linux-amd64"

		image, err := elf.NewFile(bytes.NewReader(files[name]))
		if err != nil {
			return fmt.Errorf("%s is not ELF: %w", name, err)
		}

		if image.Class != elf.ELFCLASS64 || image.Machine != elf.EM_X86_64 || (image.Type != elf.ET_EXEC && image.Type != elf.ET_DYN) {
			return fmt.Errorf("%s is not a linux/amd64 executable: %w", name, errs.ErrValidation)
		}

		_ = image.Close()
	}

	return nil
}

func (roles rustRoles) requireRegistry(t *testing.T, files map[string][]byte, commit string, target livetest.Target, repo string) {
	t.Helper()

	version := "0.1.0-dev." + commit[:12]

	host, err := livetest.RegistryHost(target)
	if err != nil {
		t.Fatal(err)
	}

	imageRef := host + "/" + livetest.RepoSlug(target, repo)
	work := t.TempDir()
	auth := livetest.RegistryAuthFile(t, target, work)

	var receipt struct {
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(files["receipt.json"], &receipt); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}

	for _, role := range []string{roles.Worker, roles.Service, roles.LoadGenerator} {
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

		paths := roles.imagePaths()[role]
		hashes := livetest.ImageFileSHA256(t, target, repo, record.Digest, paths)

		if err := verifyRustImageHashes(receipt.Files, paths, hashes); err != nil {
			t.Fatalf("image %s: %v", record.Digest, err)
		}
	}

	if tags := livetest.RegistrySnapshot(t, target, repo); len(tags) != 0 {
		t.Fatalf("tagless development created registry tags: %v", tags)
	}

	t.Logf("native receipt binds four ELF binaries and three registry runtime digests to %s (%s)", commit, version)
}

func (roles rustRoles) archive(body []byte) (map[string][]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, err
	}

	want := map[string]int64{"receipt.json": 16 << 10}

	for _, role := range []string{roles.Worker, roles.Service, roles.LoadGenerator} {
		want[role+"-image.json"] = 16 << 10
	}

	for _, binary := range []string{roles.Worker, roles.Tool, roles.Service, roles.LoadGenerator} {
		want["linux-amd64/"+binary+"-linux-amd64"] = 256 << 20
	}

	files := map[string][]byte{}
	remaining := int64(256 << 20)

	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			continue
		}

		name := strings.TrimPrefix(file.Name, "development-artifacts/")

		limit, ok := want[name]
		if !ok || files[name] != nil || !file.Mode().IsRegular() || (file.Name != name && file.Name != "development-artifacts/"+name) {
			return nil, fmt.Errorf("unexpected native artifact %s: %w", file.Name, errs.ErrValidation)
		}

		limit = min(limit, remaining)

		stream, err := file.Open()
		if err != nil {
			return nil, err
		}

		data, err := io.ReadAll(io.LimitReader(stream, limit+1))
		_ = stream.Close()

		if err != nil {
			return nil, fmt.Errorf("read artifact %s: %w", name, err)
		}

		if len(data) == 0 || int64(len(data)) > limit {
			return nil, fmt.Errorf("invalid or oversized artifact %s: %w", name, errs.ErrValidation)
		}

		remaining -= int64(len(data))
		files[name] = data
	}

	if len(files) != len(want) {
		return nil, fmt.Errorf("want exactly eight native files, got %v: %w", sortedNames(files), errs.ErrValidation)
	}

	return files, nil
}

type rustRoles struct {
	Worker, Tool, Service, LoadGenerator string
}

func rustFixtureRoles() rustRoles {
	return rustRoles{Worker: "worker", Tool: "key-tool", Service: "api-service", LoadGenerator: "load-generator"}
}

func (roles rustRoles) imagePaths() map[string][]string {
	return map[string][]string{
		roles.Worker:        {roles.Worker, "usr/local/bin/" + roles.Tool},
		roles.Service:       {roles.Service},
		roles.LoadGenerator: {"usr/local/bin/" + roles.LoadGenerator},
	}
}

func verifyRustImageHashes(receipt map[string]string, paths []string, hashes map[string]string) error {
	if len(hashes) != len(paths) {
		return fmt.Errorf("image file set differs from declared paths: %w", errs.ErrValidation)
	}

	for _, imagePath := range paths {
		hash := hashes[imagePath]
		binary := path.Base(imagePath)

		if hash == "" || receipt["linux-amd64/"+binary+"-linux-amd64"] != hash {
			return fmt.Errorf("downloaded %s differs from published image: %w", binary, errs.ErrValidation)
		}
	}

	return nil
}
