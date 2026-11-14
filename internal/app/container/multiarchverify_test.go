// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
)

func checkedIndexFixture(t *testing.T) (appcontainer.SignerImageAssembleInput, *fakeSignerImageTool) {
	t.Helper()
	t.Chdir(t.TempDir())
	writeAuthFileForSignerImage(t, "auth.json")

	in := appcontainer.SignerImageAssembleInput{AuthFile: "auth.json", SourceSHA: strings.Repeat("a", 64), ServerURL: "https://forge.example", Repository: "acme/widget", ImageRepository: "registry.example/acme/widget", TagName: "staging-v1.2.3", Name: "widget", Archs: []string{"amd64", "arm64"}, RetryAttempts: 1}
	registry := &fakeSignerImageTool{labels: map[string]map[string]string{}}

	for index, arch := range in.Archs {
		digest := "sha256:" + strings.Repeat(strconv.Itoa(index+1), 64)
		metadata := appcontainer.SignerImageArchMetadata{Arch: arch, Tag: in.ImageRepository + ":" + in.TagName + "-" + arch, Digest: digest, Ref: in.ImageRepository + "@" + digest}
		body, err := json.Marshal(metadata)
		require.NoError(t, err)

		dir := "widget-arch-" + arch
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "widget-"+arch+".json"), body, 0o600))

		registry.labels[metadata.Ref] = map[string]string{"org.example.revision": in.SourceSHA}
	}

	registry.raw = []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:%s","platform":{"os":"linux","architecture":"amd64"}},{"digest":"sha256:%s","platform":{"os":"linux","architecture":"arm64"}}]}`, strings.Repeat("1", 64), strings.Repeat("2", 64)))

	return in, registry
}

func TestMultiarchReleaseTagAndReadOnlyVerification(t *testing.T) {
	in, registry := checkedIndexFixture(t)
	in.RequiredLabels = []string{"org.example.revision=" + in.SourceSHA}
	ref := in.ImageRepository + "@" + manifestTestDigest(registry.raw)
	require.NoError(t, appcontainer.VerifyMultiarchImageManifest(t.Context(), registry, nil, ref, in))
	require.NoDirExists(t, "widget-dist")
	require.False(t, slices.Contains(registry.events, "merge"))

	in.ManifestOutput = "index.json"
	meta, err := appcontainer.AssembleSignerImageManifest(t.Context(), registry, nil, nil, io.Discard, in)
	require.NoError(t, err)
	require.Equal(t, in.ImageRepository+":staging-v1.2.3", meta.Tag)
	require.Equal(t, []string{meta.Tag}, registry.mergeTags)

	snapshot, err := os.ReadFile(in.ManifestOutput)
	require.NoError(t, err)
	require.Equal(t, registry.raw, snapshot, "manifest bytes must not gain whitespace")
	require.NoError(t, os.Remove(in.ManifestOutput))

	registry.events = nil
	require.NoError(t, appcontainer.VerifyMultiarchImageManifest(t.Context(), registry, nil, meta.Ref, in))
	snapshot, err = os.ReadFile(in.ManifestOutput)
	require.NoError(t, err)
	require.Equal(t, registry.raw, snapshot)
	require.False(t, slices.Contains(registry.events, "merge"))
	require.Error(t, appcontainer.VerifyMultiarchImageManifest(t.Context(), registry, nil, in.ImageRepository+":latest", in))
	wrong := in.ImageRepository + "@sha256:" + strings.Repeat("0", 64)
	require.NoError(t, os.Remove(in.ManifestOutput))
	require.Error(t, appcontainer.VerifyMultiarchImageManifest(t.Context(), registry, nil, wrong, in))
	require.NoFileExists(t, in.ManifestOutput)
}

func TestMultiarchBuildExplicitTagPreservesSourceIdentity(t *testing.T) {
	t.Chdir(t.TempDir())
	writeAuthFileForSignerImage(t, "auth.json")

	tool := &fakeSignerImageTool{}
	source := strings.Repeat("a", 64)
	metadata, err := appcontainer.BuildSignerImageArch(t.Context(), tool, io.Discard, appcontainer.SignerImageBuildArchInput{
		AuthFile: "auth.json", Arch: "arm64", HostArch: "arm64", SourceSHA: source,
		ServerURL: "https://forge.example", Repository: "acme/widget", ImageRepository: "registry.example/acme/widget",
		TagName: "staging-v1.2.3", Name: "widget", RetryAttempts: 1,
	})
	require.NoError(t, err)
	require.Equal(t, "registry.example/acme/widget:staging-v1.2.3-arm64", metadata.Tag)
	require.Equal(t, []string{metadata.Tag}, tool.pushes)
	require.Equal(t, source, tool.buildReqs[0].Revision)
	require.Equal(t, "https://forge.example/acme/widget", tool.buildReqs[0].SourceURL)
}

func TestMultiarchRequiredLabelsFailBeforePublication(t *testing.T) {
	for _, failure := range []string{"missing", "wrong", "read-error", "invalid-constraint"} {
		t.Run(failure, func(t *testing.T) {
			in, registry := checkedIndexFixture(t)
			in.RequiredLabels = []string{"org.example.revision=" + in.SourceSHA}
			in.ManifestOutput = "snapshot.json"

			switch failure {
			case "missing":
				registry.labels = nil
			case "wrong":
				registry.labels[in.ImageRepository+"@sha256:"+strings.Repeat("2", 64)]["org.example.revision"] = "other"
			case "read-error":
				registry.failOn = "labels"
			case "invalid-constraint":
				in.RequiredLabels = []string{"missing-equals"}
			}

			_, err := appcontainer.AssembleSignerImageManifest(t.Context(), registry, nil, nil, io.Discard, in)
			require.Error(t, err)
			require.Empty(t, registry.mergeTags)
			require.NoFileExists(t, in.ManifestOutput)
		})
	}
}

func TestMultiarchExplicitTagRejectsConflictsBeforeRegistryAccess(t *testing.T) {
	for _, tag := range []string{"bad/tag", "../escape", "tag\nother", strings.Repeat("x", 128)} {
		t.Run(tag, func(t *testing.T) {
			in, registry := checkedIndexFixture(t)
			in.TagName = tag
			_, err := appcontainer.AssembleSignerImageManifest(t.Context(), registry, nil, nil, io.Discard, in)
			require.Error(t, err)
			require.Empty(t, registry.events)
		})
	}

	in, registry := checkedIndexFixture(t)
	in.TagPrefix = "another-"
	_, err := appcontainer.AssembleSignerImageManifest(t.Context(), registry, nil, nil, io.Discard, in)
	require.Error(t, err)
	require.Empty(t, registry.events)
}

func TestMultiarchSnapshotCannotClobberInputsOrMetadata(t *testing.T) {
	for _, path := range []string{"-", "auth.json", "widget-arch-amd64/widget-amd64.json", "widget-dist/widget.json", "widget-dist"} {
		t.Run(path, func(t *testing.T) {
			in, registry := checkedIndexFixture(t)
			in.ManifestOutput = path
			_, err := appcontainer.AssembleSignerImageManifest(t.Context(), registry, nil, nil, io.Discard, in)
			require.Error(t, err)
			require.Empty(t, registry.events)
		})
	}

	in, registry := checkedIndexFixture(t)
	require.NoError(t, os.Symlink("widget-dist/widget.json", "snapshot.json"))

	in.ManifestOutput = "snapshot.json"
	_, err := appcontainer.AssembleSignerImageManifest(t.Context(), registry, nil, nil, io.Discard, in)
	require.Error(t, err)
	require.Empty(t, registry.events)
}

func TestMultiarchSnapshotRejectsAliasedMetadataParentsBeforePublication(t *testing.T) {
	for _, existing := range []bool{true, false} {
		t.Run(fmt.Sprintf("existing-parent=%t", existing), func(t *testing.T) {
			in, registry := checkedIndexFixture(t)
			if existing {
				require.NoError(t, os.Mkdir("widget-dist", 0o700))
				require.NoError(t, os.WriteFile("widget-dist/keep", []byte("previous evidence"), 0o600))
			}

			require.NoError(t, os.Symlink("widget-dist", "alias"))

			in.ManifestOutput = "alias/widget.json"
			_, err := appcontainer.AssembleSignerImageManifest(t.Context(), registry, nil, nil, io.Discard, in)
			require.Error(t, err)
			require.Empty(t, registry.events)
			require.NoFileExists(t, "widget-dist/widget.json")

			if existing {
				body, readErr := os.ReadFile("widget-dist/keep")
				require.NoError(t, readErr)
				require.Equal(t, "previous evidence", string(body))
			}
		})
	}
}

func TestMultiarchSnapshotSupportsNoncollidingParents(t *testing.T) {
	for _, linked := range []bool{true, false} {
		t.Run(fmt.Sprintf("linked-parent=%t", linked), func(t *testing.T) {
			in, registry := checkedIndexFixture(t)
			if linked {
				require.NoError(t, os.Mkdir("snapshots", 0o700))
				require.NoError(t, os.Symlink("snapshots", "alias"))

				in.ManifestOutput = "alias/index.json"
			} else {
				in.MetadataDir = "artifacts/widget-dist"
				in.ManifestOutput = "artifacts/widget-dist/index.json"
			}

			_, err := appcontainer.AssembleSignerImageManifest(t.Context(), registry, nil, nil, io.Discard, in)
			require.NoError(t, err)
			body, err := os.ReadFile(in.ManifestOutput)
			require.NoError(t, err)
			require.Equal(t, registry.raw, body)
		})
	}
}
