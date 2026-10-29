// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/artifact"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestSnapshotDeliveredArtifactBundle(t *testing.T) {
	root := os.Getenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE")
	source := os.Getenv("FORGE_CI_LIVE_SNAPSHOT_SOURCE_SHA")

	if root == "" || source == "" {
		t.Skip("select an existing delivered evidence bundle and its expected source commit")
	}

	trees := map[string]map[string][]byte{}

	for _, name := range []string{"inputs", "context", "images"} {
		filename := "github-snapshot-images-" + name + ".zip"
		if name == "images" {
			filename = "github-snapshot-images.zip"
		}

		body, err := os.ReadFile(filepath.Join(root, filename))
		if err != nil {
			t.Fatal(err)
		}

		tree, err := ZIPTree(body, 64<<20)
		if err != nil {
			t.Fatal(err)
		}

		trees[name] = tree
	}

	var plan snapshotimage.Plan
	if err := json.Unmarshal(trees["inputs"]["snapshot-plan.json"], &plan); err != nil || len(plan.Images) != 1 {
		t.Fatal("missing actual delivered plan")
	}

	receipt, file, err := checkSnapshotArtifactEvidence(trees["inputs"]["snapshot-plan.json"], trees["images"]["container-images.json"], trees["context"]["files/snapshot-dependencies.json"], source, plan.Images[0].ImageRepository)
	if err != nil {
		t.Fatal(err)
	}

	actual := trees["context"]["files/"+file.Path]
	if int64(len(actual)) != file.Size || fmt.Sprintf("%x", sha256.Sum256(actual)) != file.SHA256 {
		t.Fatal("actual delivered executable differs from recorded mode/digest inventory")
	}

	t.Logf("actual bundle verified offline: source=%s ref=%s producer-mode=%s sha256=%s", source, receipt.Ref, file.Mode, file.SHA256)
}

func TestSnapshotWorkingTreeNativeBuild(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("native linux/amd64 snapshot preflight")
	}

	dir := t.TempDir()
	snapshotWriteFiles(t, dir, snapshotCandidateFiles(t))
	output := filepath.Join(t.TempDir(), "forge-ci")
	command := exec.CommandContext(t.Context(), "go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -X main.version=engine-layer", "-o", output, "./cmd/forge-ci")
	command.Dir = dir

	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("candidate snapshot does not build: %v: %s", err, out)
	}

	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}

	record, err := os.ReadFile(filepath.Join(dir, ".engine/bin/forge-ci-linux-amd64.sha256"))
	if err != nil {
		t.Fatal(err)
	}

	if got := fmt.Sprintf("%x", sha256.Sum256(body)); !strings.HasPrefix(string(record), got+"  ") {
		t.Fatal("working-tree snapshot binary does not match recorded native engine checksum")
	}

	t.Logf("working-tree candidate build matches engine checksum: %x", sha256.Sum256(body))
}

func saveSnapshotEvidence(t *testing.T, name string, body []byte) {
	t.Helper()

	root := os.Getenv("FORGE_CI_LIVE_SNAPSHOT_EVIDENCE")
	if root == "" {
		return
	}

	if !filepath.IsAbs(root) || filepath.Base(name) != name {
		t.Fatal("snapshot evidence requires an absolute root and plain filename")
	}

	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, name), body, 0600); err != nil {
		t.Fatal(err)
	}
}

// Only explicit production-source/helper roots are copied. Never copy ambient
// credentials, generated binaries, controller recordings or active release jobs.
func snapshotCandidateFiles(t *testing.T) map[string]filePayload {
	t.Helper()
	root := reporoot.Path(t)
	args := []string{"-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "cmd", "internal", "scripts/bootstrap", "go.mod", "go.sum", "LICENSE", "LICENSES", "REUSE.toml", ".engine/bin/lib-forge-ci.sh", ".engine/bin/forge-ci-linux-amd64.sha256", ".engine/bin/forge-ci-linux-arm64.sha256", ".forgejo/actions", ".forgejo/bin", ".forgejo/workflows/snapshot-images.yml", ".gitlab/bin", "templates/snapshot-images.yml", "templates/snapshot-image-jobs.yml", ".github/workflows/snapshot-images.yml", ".github/workflows/resolve-forge-ci-bootstrap.yml"}

	out, err := exec.CommandContext(t.Context(), "git", args...).Output()
	if err != nil {
		t.Fatal(err)
	}

	files := map[string]filePayload{}

	for path := range strings.SplitSeq(string(out), "\x00") {
		if path == "" || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "/testdata/") {
			continue
		}

		info, err := os.Lstat(filepath.Join(root, path))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("candidate source not regular: %s", path)
		}

		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}

		mode, err := strconv.ParseUint(artifact.CanonicalFileMode(info.Mode()), 8, 32)
		if err != nil {
			t.Fatal(err)
		}
		files[path] = filePayload{Body: string(body), Mode: fs.FileMode(mode)}
	}

	return files
}

func snapshotWriteFiles(t *testing.T, dir string, files map[string]filePayload) {
	t.Helper()

	for path, payload := range files {
		name := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(payload.Body), payload.Mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(name, payload.Mode); err != nil {
			t.Fatal(err)
		}
	}
	// Verify the actual publication tree before callers can push it.
	for path, payload := range files {
		name := filepath.Join(dir, path)
		body, err := os.ReadFile(name)
		if err != nil || string(body) != payload.Body {
			t.Fatalf("snapshot bytes differ: %s: %v", path, err)
		}
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != payload.Mode {
			t.Fatalf("snapshot mode differs: %s: %v", path, err)
		}
	}
}

func snapshotRegistryCanary(t *testing.T, ctx context.Context, ref, tag, username, password, expectedHash, source string, extra ...remote.Option) {
	t.Helper()

	digest, err := name.NewDigest(ref)
	if err != nil {
		t.Fatal(err)
	}

	options := make([]remote.Option, 0, 3+len(extra))
	options = append(options, remote.WithContext(ctx), remote.WithAuth(&authn.Basic{Username: username, Password: password}))
	options = append(options, extra...)
	options = append(options, remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}))

	repository, _, _ := strings.Cut(ref, "@")

	imageTag, err := name.NewTag(repository + ":" + tag)
	if err != nil {
		t.Fatal(err)
	}

	served, err := remote.Head(imageTag, options...)
	if err != nil || repository+"@"+served.Digest.String() != ref {
		t.Fatal("registry-served snapshot tag differs from immutable receipt")
	}

	repositoryName, err := name.NewRepository(repository)
	if err != nil {
		t.Fatal(err)
	}

	tags, err := remote.List(repositoryName, options...)
	if err != nil || len(tags) == 0 || len(tags) > 2 {
		t.Fatal("cannot verify bounded snapshot-only registry tags")
	}

	for _, actual := range tags {
		if actual != tag && actual != tag+"-amd64" {
			t.Fatal("snapshot publication wrote an unexpected stable alias")
		}
	}

	image, err := remote.Image(digest, options...)
	if err != nil {
		t.Fatal("cannot read exact published snapshot image")
	}

	stream := mutate.Extract(image)
	defer func() { _ = stream.Close() }()

	archive := tar.NewReader(io.LimitReader(stream, 64<<20))

	for {
		entry, err := archive.Next()
		if err != nil {
			t.Fatal("published snapshot lacks canary")
		}

		if strings.TrimPrefix(entry.Name, "./") != "canary" {
			continue
		}

		if entry.Typeflag != tar.TypeReg || entry.Size <= 0 || entry.Size > 32<<20 || entry.Mode&0111 == 0 {
			t.Fatal("registry canary is not a bounded executable")
		}

		body, err := io.ReadAll(io.LimitReader(archive, entry.Size))
		if err != nil || int64(len(body)) != entry.Size || fmt.Sprintf("%x", sha256.Sum256(body)) != expectedHash {
			t.Fatal("registry executable differs from verified producer inventory")
		}

		executable := filepath.Join(t.TempDir(), "canary")
		if err := os.WriteFile(executable, body, 0500); err != nil {
			t.Fatal(err)
		}

		command := exec.CommandContext(ctx, executable)
		command.Env = []string{"LC_ALL=C"}

		output, err := command.Output()
		if err != nil || string(output) != "selected-source|"+source+"\n" {
			t.Fatal("actual registry executable does not report selected source commit")
		}

		t.Logf("registry executable verified: ref=%s sha256=%s source=%s native=linux/amd64", ref, expectedHash, source)

		return
	}
}
