// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/stretchr/testify/require"
)

type runtimeDownloadFunc func(context.Context, provider.RunArtifactDownload) (provider.RunArtifactInfo, error)

func (f runtimeDownloadFunc) DownloadRunArtifact(ctx context.Context, in provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
	return f(ctx, in)
}

func runtimeCollectInput(t *testing.T) (string, appcontainer.RuntimePublicationInput) {
	t.Helper()

	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".engine/bin"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".engine/bin/forge-ci-linux-amd64.sha256"), []byte(strings.Repeat("d", 64)+"  bin/forge-ci\n"), 0o600))
	roster := filepath.Join(workspace, "images.json")
	require.NoError(t, os.WriteFile(roster, []byte(`[{"target":"go","name":"runtime-go","archs":["amd64"]}]`), 0o600))

	sha := strings.Repeat("a", 40)

	return workspace, appcontainer.RuntimePublicationInput{
		RosterFile: roster, PlatformMatrix: `[{"platform":"linux/amd64","suffix":"amd64"}]`,
		Namespace: "registry.example/acme", SourceSHA: sha, CheckedOutSHA: sha,
		RunID: "123-1", Ref: "refs/heads/main",
	}
}

func TestRuntimeCollectBindsCandidateMetadataBeforeSchedulingSmoke(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ name, field, value string }{
		{name: "valid"},
		{"foreign registry", "ref", "registry.other/acme/runtime-go@sha256:" + strings.Repeat("b", 64)},
		{"wrong digest", "digest", "sha256:" + strings.Repeat("c", 64)},
		{"wrong architecture", "arch", "arm64"},
		{"another run", "tag", "registry.example/acme/runtime-go:staging-other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			workspace, in := runtimeCollectInput(t)
			sink := fakeoutputsink.New(t)
			download := runtimeDownloadFunc(func(_ context.Context, request provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
				require.Equal(t, "runtime-go-arch-amd64", request.Name)
				require.NoError(t, os.MkdirAll(request.Dir, 0o750))

				metadata := map[string]string{
					"arch": "amd64", "tag": in.Namespace + "/runtime-go:staging-123-1-aaaaaaaaaaaa-amd64",
					"digest": "sha256:" + strings.Repeat("b", 64), "ref": in.Namespace + "/runtime-go@sha256:" + strings.Repeat("b", 64),
				}
				if test.field != "" {
					metadata[test.field] = test.value
				}

				body, err := json.Marshal(metadata)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(request.Dir, "runtime-go-amd64.json"), body, 0o600))

				return provider.RunArtifactInfo{}, nil
			})

			err := appcontainer.CollectRuntimePublication(t.Context(), download, sink, workspace, in)
			if test.field != "" {
				require.Error(t, err)
				require.Empty(t, sink.Keys())
			} else {
				require.NoError(t, err)
				require.JSONEq(t, `{"include":[{"target":"go","arch":"amd64","ref":"registry.example/acme/runtime-go@sha256:`+strings.Repeat("b", 64)+`","checksum":"`+strings.Repeat("d", 64)+`"}]}`, sink.Single("smoke-matrix"))
			}

			leftovers, err := filepath.Glob(filepath.Join(workspace, ".runtime-metadata-*"))
			require.NoError(t, err)
			require.Empty(t, leftovers)
		})
	}
}

func TestRuntimeCollectAdmitsAllInputsBeforeDownloads(t *testing.T) {
	t.Parallel()

	for _, test := range []string{"empty", "object", "unknown", "duplicate", "mixed", "invalid", "checksum", "source", "ref"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()

			workspace, in := runtimeCollectInput(t)

			switch test {
			case "empty":
				in.PlatformMatrix = `[]`
			case "object":
				in.PlatformMatrix = `{}`
			case "unknown":
				in.PlatformMatrix = `[{"platform":"linux/s390x","suffix":"s390x"}]`
			case "duplicate":
				in.PlatformMatrix = `[{"platform":"linux/amd64","suffix":"amd64"},{"platform":"linux/amd64","suffix":"amd64"}]`
			case "mixed":
				in.PlatformMatrix = `[{"platform":"linux/amd64","suffix":"amd64"},{"platform":"linux/armv7","suffix":"armv7"}]`
			case "invalid":
				in.PlatformMatrix = `invalid-json`
			case "checksum":
				require.NoError(t, os.WriteFile(filepath.Join(workspace, ".engine/bin/forge-ci-linux-amd64.sha256"), []byte("invalid"), 0o600))
			case "source":
				in.CheckedOutSHA = strings.Repeat("b", 40)
			case "ref":
				in.Ref = "refs/heads/topic"
			}

			sink := fakeoutputsink.New(t)
			download := runtimeDownloadFunc(func(context.Context, provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
				t.Error("refused input reached artifact transport")

				return provider.RunArtifactInfo{}, nil
			})
			err := appcontainer.CollectRuntimePublication(t.Context(), download, sink, workspace, in)
			require.Error(t, err)
			require.Empty(t, sink.Keys())
		})
	}
}

func TestRuntimeCollectDownloadFailurePreservesErrorAndCleansWorkspace(t *testing.T) {
	t.Parallel()

	workspace, in := runtimeCollectInput(t)
	want := errors.New("artifact unavailable")
	sink := fakeoutputsink.New(t)
	download := runtimeDownloadFunc(func(context.Context, provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
		return provider.RunArtifactInfo{}, want
	})
	err := appcontainer.CollectRuntimePublication(t.Context(), download, sink, workspace, in)
	require.ErrorIs(t, err, want)
	require.Empty(t, sink.Keys())

	leftovers, err := filepath.Glob(filepath.Join(workspace, ".runtime-metadata-*"))
	require.NoError(t, err)
	require.Empty(t, leftovers)
}

func TestRuntimeCollectNeverEmitsAPartialNativeSet(t *testing.T) {
	t.Parallel()

	workspace, in := runtimeCollectInput(t)
	in.PlatformMatrix = `[{"platform":"linux/amd64","suffix":"amd64"},{"platform":"linux/arm64","suffix":"arm64"}]`
	require.NoError(t, os.WriteFile(in.RosterFile, []byte(`[{"target":"go","name":"runtime-go","archs":["amd64","arm64"]}]`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".engine/bin/forge-ci-linux-arm64.sha256"), []byte(strings.Repeat("e", 64)+"  bin/forge-ci\n"), 0o600))
	sink := fakeoutputsink.New(t)

	var calls []string

	download := runtimeDownloadFunc(func(_ context.Context, request provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
		calls = append(calls, request.Name)
		require.NoError(t, os.MkdirAll(request.Dir, 0o750))
		arch := strings.TrimPrefix(request.Name, "runtime-go-arch-")

		metadata := appcontainer.SignerImageArchMetadata{
			Arch: arch, Tag: in.Namespace + "/runtime-go:staging-123-1-aaaaaaaaaaaa-" + arch,
			Digest: "sha256:" + strings.Repeat("b", 64), Ref: in.Namespace + "/runtime-go@sha256:" + strings.Repeat("b", 64),
		}
		if arch == "arm64" {
			metadata.Ref = "unrelated bytes"
		}

		body, err := json.Marshal(metadata)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(request.Dir, "runtime-go-"+arch+".json"), body, 0o600))

		return provider.RunArtifactInfo{}, nil
	})
	err := appcontainer.CollectRuntimePublication(t.Context(), download, sink, workspace, in)
	require.Error(t, err)
	require.Equal(t, []string{"runtime-go-arch-amd64", "runtime-go-arch-arm64"}, calls)
	require.Empty(t, sink.Keys())
}

func TestRuntimeCollectCancellationPrecedesReadsAndDownloads(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	sink := fakeoutputsink.New(t)
	download := runtimeDownloadFunc(func(context.Context, provider.RunArtifactDownload) (provider.RunArtifactInfo, error) {
		t.Error("cancelled collection reached artifact transport")

		return provider.RunArtifactInfo{}, nil
	})
	err := appcontainer.CollectRuntimePublication(ctx, download, sink, "must-not-be-created", appcontainer.RuntimePublicationInput{RosterFile: "must-not-be-read"})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, sink.Keys())
}
