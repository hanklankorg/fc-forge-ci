// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

type scanner struct {
	t        *testing.T
	calls    int
	fail     bool
	severity string
}

func (s *scanner) RunInherit(_ context.Context, _, _ io.Writer, args ...string) (int, error) {
	s.calls++
	index := slices.Index(args, "--severity")
	require.GreaterOrEqual(s.t, index, 0)
	require.Equal(s.t, s.severity, args[index+1])
	index = slices.Index(args, "--output")
	require.GreaterOrEqual(s.t, index, 0)
	body := `{"Results":[]}`
	if s.fail {
		body = `{"Results":[{"Target":"fixture","Vulnerabilities":[{"VulnerabilityID":"CVE-fixture","PkgName":"bad","InstalledVersion":"1","FixedVersion":"2","Severity":"HIGH"}]}]}`
	}
	require.NoError(s.t, os.WriteFile(args[index+1], []byte(body), 0o600))
	return 0, nil
}

type inventoryTool struct {
	t         *testing.T
	calls     int
	malformed bool
}

func (s *inventoryTool) Generate(_ context.Context, target string, outputs map[string]string, _ io.Writer) error {
	s.calls++
	require.True(s.t, strings.HasPrefix(target, "oci-dir:"))
	body := `{"bomFormat":"CycloneDX","specVersion":"1.6","version":1}`
	if s.malformed {
		body = `{"bomFormat":"wrong"}`
	}
	require.NoError(s.t, os.WriteFile(outputs["cyclonedx-json"], []byte(body), 0o600))
	return nil
}

func publicationFixture(t *testing.T) (snapshotimage.Plan, appsnapshot.PublishInput, *atomic.Int32) {
	t.Helper()
	writes := new(atomic.Int32)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writes.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	t.Chdir(root)
	auth := filepath.Join(root, "auth.json")
	require.NoError(t, os.WriteFile(auth, []byte(`{"auths":{}}`), 0o600))
	sha := strings.Repeat("a", 40)
	tag, err := version.ComposeSnapshotContainerTag("1.2.3", "manual", "aaaaaaa")
	require.NoError(t, err)
	repo := strings.TrimPrefix(server.URL, "http://") + "/team/app"
	im := snapshotimage.Image{Name: "app", ImageRepository: repo, Tag: tag, Platforms: []string{"linux/amd64", "linux/arm64"}, Severity: "HIGH", Scan: true, Signing: "NONE"}
	p := snapshotimage.Plan{Version: 1, Enabled: true, SourceSHA: sha, SourceBranch: "manual", BaseVersion: "1.2.3", Tag: tag, Images: []snapshotimage.Image{im}, Builds: []snapshotimage.Build{}}
	for _, arch := range []string{"amd64", "arm64"} {
		p.Builds = append(p.Builds, snapshotimage.Build{Name: "app", Arch: arch, ImageRepository: repo, Tag: tag, ContainerFile: "Containerfile", Context: "."})
		config, err := empty.Image.ConfigFile()
		require.NoError(t, err)
		config.OS = "linux"
		config.Architecture = arch
		config.Config.Labels = map[string]string{"org.opencontainers.image.revision": sha, "org.opencontainers.image.version": tag, "org.opencontainers.image.ref.name": "manual"}
		image, err := mutate.ConfigFile(empty.Image, config)
		require.NoError(t, err)
		dir := filepath.Join(root, "gathered", "app-arch-"+arch)
		path, err := layout.Write(filepath.Join(dir, "layout"), empty.Index)
		require.NoError(t, err)
		require.NoError(t, path.AppendImage(image))
		ref := repo + ":" + tag + "-" + arch
		record, err := appcontainer.RecordImageLayout(t.Context(), ociregistry.NewAnonymous(), appcontainer.LayoutRecordInput{Directory: filepath.Join(dir, "layout"), ImageRef: ref, Platform: "linux/" + arch, SourceSHA: sha})
		require.NoError(t, err)
		require.NoError(t, appsnapshot.WriteJSON(filepath.Join(dir, "app-"+arch+".json"), appcontainer.SignerImageArchMetadata{Arch: arch, Tag: ref, Digest: record.Digest, Ref: repo + "@" + record.Digest}))
	}
	require.NoError(t, p.Validate())
	return p, appsnapshot.PublishInput{Name: "app", MetadataDir: filepath.Join(root, "gathered"), AuthFile: auth, ServerURL: server.URL, Repository: "team/app", EvidenceDir: "evidence"}, writes
}

func TestPublicationCompleteNativeSetAndScanBarrier(t *testing.T) {
	for _, defect := range []string{"missing sibling", "wrong source", "wrong platform", "failed scan", "failed SBOM", "missing scanner", "missing SBOM tool", "symlink metadata", "nonregular metadata", "unknown metadata field"} {
		t.Run(defect, func(t *testing.T) {
			p, in, writes := publicationFixture(t)
			scan := &scanner{t: t, severity: "HIGH", fail: defect == "failed scan"}
			syft := &inventoryTool{t: t, malformed: defect == "failed SBOM"}
			if defect == "failed SBOM" || defect == "missing SBOM tool" {
				p.Images[0].SBOM = true
			}
			priorSBOM := filepath.Join(in.EvidenceDir, "app", "linux-amd64", "sbom.cyclonedx.json")
			if defect == "failed SBOM" {
				require.NoError(t, os.MkdirAll(filepath.Dir(priorSBOM), 0o700))
				require.NoError(t, os.WriteFile(priorSBOM, []byte("prior complete inventory"), 0o600))
			}
			if defect == "missing sibling" {
				require.NoError(t, os.RemoveAll(filepath.Join(in.MetadataDir, "app-arch-arm64")))
			}
			if defect == "wrong source" {
				replaceStagedIdentity(t, p, in, "arm64", "arm64", strings.Repeat("b", 40))
			}
			if defect == "wrong platform" {
				replaceStagedIdentity(t, p, in, "arm64", "amd64", p.SourceSHA)
			}
			metadata := filepath.Join(in.MetadataDir, "app-arch-arm64", "app-arm64.json")
			switch defect {
			case "missing scanner":
				scan = nil
			case "missing SBOM tool":
				syft = nil
			case "symlink metadata":
				require.NoError(t, os.Rename(metadata, metadata+".real"))
				require.NoError(t, os.Symlink(metadata+".real", metadata))
			case "nonregular metadata":
				require.NoError(t, os.Remove(metadata))
				require.NoError(t, os.Mkdir(metadata, 0o700))
			case "unknown metadata field":
				body, err := os.ReadFile(metadata)
				require.NoError(t, err)
				body = append([]byte(`{"unknown":true,`), body[1:]...)
				require.NoError(t, os.WriteFile(metadata, body, 0o600))
			}
			// Convert nil concrete pointers to nil interfaces for dependency checks.
			var scannerOps appcontainer.ImageEvidenceTrivy
			if scan != nil {
				scannerOps = scan
			}
			var syftOps appcontainer.ImageEvidenceSyft
			if syft != nil {
				syftOps = syft
			}
			_, err := appsnapshot.Publish(t.Context(), ociregistry.WithAuthFile(in.AuthFile), scannerOps, syftOps, fakeoutputsink.New(t), nil, io.Discard, output.Annotator{}, p, in)
			require.Error(t, err)
			require.Zero(t, writes.Load(), fmt.Sprint(err))
			if defect == "failed scan" {
				require.Equal(t, 2, scan.calls, "every platform is gated before upload")
			}
			if defect == "failed SBOM" {
				require.Equal(t, 2, syft.calls)
				body, readErr := os.ReadFile(priorSBOM)
				require.NoError(t, readErr)
				require.Equal(t, "prior complete inventory", string(body))
			}
			if defect == "wrong source" || defect == "wrong platform" {
				require.Zero(t, scan.calls, "source/platform binding must precede callbacks")
				require.Zero(t, syft.calls)
			}
		})
	}
}

// Forge-native metadata can be internally consistent and still lie about the
// content's source/platform. Verification must inspect the staged config bytes.
func replaceStagedIdentity(t *testing.T, p snapshotimage.Plan, in appsnapshot.PublishInput, arch, actualArch, revision string) {
	t.Helper()
	im := p.Images[0]
	config, err := empty.Image.ConfigFile()
	require.NoError(t, err)
	config.OS = "linux"
	config.Architecture = actualArch
	config.Config.Labels = map[string]string{"org.opencontainers.image.revision": revision, "org.opencontainers.image.version": im.Tag, "org.opencontainers.image.ref.name": p.SourceBranch}
	image, err := mutate.ConfigFile(empty.Image, config)
	require.NoError(t, err)
	directory := filepath.Join(in.MetadataDir, "app-arch-"+arch)
	path, err := layout.Write(filepath.Join(directory, "layout"), empty.Index)
	require.NoError(t, err)
	require.NoError(t, path.AppendImage(image))
	digest, err := image.Digest()
	require.NoError(t, err)
	tag := im.ImageRepository + ":" + im.Tag + "-" + arch
	require.NoError(t, appsnapshot.WriteJSON(filepath.Join(directory, "app-"+arch+".json"), appcontainer.SignerImageArchMetadata{Arch: arch, Tag: tag, Digest: digest.String(), Ref: im.ImageRepository + "@" + digest.String()}))
	require.NoError(t, appsnapshot.WriteJSON(filepath.Join(directory, "layout", "forge-ci-image.json"), appcontainer.ImageLayoutRecord{ImageRef: tag, Platform: "linux/" + arch, SourceSHA: p.SourceSHA, Digest: digest.String()}))
}

func TestSnapshotSBOMIndependentOfScanAndReceiptBindsServedIndex(t *testing.T) {
	p, in, writes := publicationFixture(t)
	p.Images[0].Scan = false
	p.Images[0].SBOM = true
	syft := &inventoryTool{t: t}
	r, err := appsnapshot.Publish(t.Context(), ociregistry.WithAuthFile(in.AuthFile), nil, syft, fakeoutputsink.New(t), nil, io.Discard, output.Annotator{}, p, in)
	require.NoError(t, err)
	require.Equal(t, 2, syft.calls)
	require.Positive(t, writes.Load())
	_, err = snapshotimage.Collect(p, []snapshotimage.Receipt{r})
	require.NoError(t, err)
	manifest, err := ociregistry.NewAnonymous().Manifest(t.Context(), r.Ref)
	require.NoError(t, err)
	var index struct {
		Manifests []json.RawMessage `json:"manifests"`
	}
	require.NoError(t, json.Unmarshal(manifest, &index))
	require.Len(t, index.Manifests, 2)
	for _, arch := range []string{"amd64", "arm64"} {
		require.FileExists(t, filepath.Join(in.EvidenceDir, "app", "linux-"+arch, "sbom.cyclonedx.json"))
	}
}
