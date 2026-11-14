// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/artifact"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

type stagingSignerTool struct {
	fakeSignerImageTool

	t *testing.T
}

func (tool *stagingSignerTool) ExportLocalImageToLayout(ctx context.Context, image, directory string, out io.Writer) error {
	if err := tool.fakeSignerImageTool.ExportLocalImageToLayout(ctx, image, directory, out); err != nil {
		return err
	}

	request := tool.buildReqs[0]
	_, _ = stagedLayout(tool.t, ctx, directory, "registry.example/org/image", strings.TrimPrefix(request.Platform, "linux/"))

	return nil
}

func TestSignerStagingDefersAllRegistryOperations(t *testing.T) {
	t.Chdir(t.TempDir()) // signer metadata is workspace-relative.
	require.NoError(t, os.WriteFile("auth.json", []byte(`{"auths":{}}`), 0o600))
	tool := &stagingSignerTool{t: t}
	metadata, err := appcontainer.StageSignerImageArch(t.Context(), tool, ociregistry.NewAnonymous(), io.Discard, appcontainer.SignerImageBuildArchInput{
		AuthFile: "auth.json", Arch: "amd64", HostArch: "amd64", SourceSHA: strings.Repeat("b", 40), ServerURL: "https://registry.example", Repository: "org/image",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"build", "export"}, tool.events)
	require.Empty(t, tool.pushes)
	require.FileExists(t, "image-arch-amd64/image-amd64.json")
	image, err := ociregistry.NewAnonymous().InspectLayout(t.Context(), "image-arch-amd64/layout")
	require.NoError(t, err)
	require.Equal(t, image.Digest, metadata.Digest)
}

// Staging writes no registry, so a build of public bases needs no auth file;
// a build that pushes still does.
func TestSignerStagingNeedsNoAuthFile(t *testing.T) {
	t.Chdir(t.TempDir()) // signer metadata is workspace-relative.

	in := appcontainer.SignerImageBuildArchInput{
		Arch: "amd64", HostArch: "amd64", SourceSHA: strings.Repeat("b", 40), ServerURL: "https://registry.example", Repository: "org/image",
	}

	tool := &stagingSignerTool{t: t}
	_, err := appcontainer.StageSignerImageArch(t.Context(), tool, ociregistry.NewAnonymous(), io.Discard, in)
	require.NoError(t, err)

	// The build gets an auth file without credentials, never the runner's.
	anonymous := tool.buildReqs[0].AuthFile
	require.NotEmpty(t, anonymous)
	require.NoFileExists(t, anonymous, "the anonymous auth file outlives the build")

	_, err = appcontainer.BuildSignerImageArch(t.Context(), &fakeSignerImageTool{}, io.Discard, in)
	require.ErrorIs(t, err, errs.ErrUsage)
	require.ErrorContains(t, err, "auth-file")
}

func TestStagedSignerAssemblyPreservesContractAndRejectsBadSibling(t *testing.T) {
	for _, scenario := range []string{"valid", "bad sibling", "required label", "duplicate architecture"} {
		t.Run(scenario, func(t *testing.T) {
			t.Chdir(t.TempDir()) // existing signer artifact convention.

			var writes atomic.Int32

			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet && r.Method != http.MethodHead {
					writes.Add(1)
				}

				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			require.NoError(t, os.WriteFile("auth.json", []byte(`{"auths":{}}`), 0o600))

			repo := strings.TrimPrefix(server.URL, "http://") + "/org/repo-signer"
			for _, arch := range []string{"amd64", "arm64"} {
				_, image := stagedLayout(t, t.Context(), "signer-image-arch-"+arch+"/layout", repo+":signer-"+strings.Repeat("b", 40)+"-"+arch, arch)
				digest, err := image.Digest()
				require.NoError(t, err)
				writeSignerArchMetadata(t, arch, repo, digest.Hex)
			}

			if scenario == "bad sibling" {
				require.NoError(t, os.WriteFile("signer-image-arch-arm64/layout/oci-layout", []byte("bad"), 0o600))
			}

			sink := fakeoutputsink.New(t)

			input := appcontainer.SignerImageAssembleInput{
				AuthFile: "auth.json", SourceSHA: strings.Repeat("b", 40), ServerURL: server.URL, Repository: "org/repo", RepositorySuffix: "-signer", TagPrefix: "signer-", Name: "signer-image", RetryAttempts: 1,
				RequiredLabels: []string{"org.opencontainers.image.revision=" + strings.Repeat("b", 40)},
			}
			if scenario == "required label" {
				input.RequiredLabels = []string{"org.example.missing=required"}
			}

			if scenario == "duplicate architecture" {
				input.Archs = []string{"amd64", "amd64"}
			}

			metadata, err := appcontainer.PublishAndAssembleSignerImage(t.Context(), ociregistry.WithAuthFile("auth.json"), sink, nil, io.Discard, input)
			if scenario != "valid" {
				require.Error(t, err)
				require.Zero(t, writes.Load())
				require.Empty(t, sink.Keys())

				return
			}

			require.NoError(t, err)
			require.Positive(t, writes.Load())
			require.Equal(t, metadata.Ref, sink.Single("image-ref"))
			require.Equal(t, metadata.Digest, sink.Single("image-digest"))
			require.Equal(t, metadata.Tag, sink.Single("image-tag"))
			require.FileExists(t, "signer-image-dist/signer-image.json")
		})
	}
}

// The publishing job scans every staged architecture before its first upload:
// a blocked architecture publishes nothing, and the evidence of every
// architecture is written whichever one fails.
func TestStagedSignerAssemblyScansEveryArchitectureBeforeUpload(t *testing.T) {
	for _, blocked := range []string{"", "linux/amd64", "sbom"} {
		t.Run("blocked="+blocked, func(t *testing.T) {
			t.Chdir(t.TempDir()) // existing signer artifact convention.

			var writes atomic.Int32

			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet && r.Method != http.MethodHead {
					writes.Add(1)
				}

				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			require.NoError(t, os.WriteFile("auth.json", []byte(`{"auths":{}}`), 0o600))

			repo := strings.TrimPrefix(server.URL, "http://") + "/org/repo"
			for _, arch := range []string{"amd64", "arm64"} {
				_, image := stagedLayout(t, t.Context(), "image-arch-"+arch+"/layout", repo+":staging-v1.2.3-"+arch, arch)
				digest, err := image.Digest()
				require.NoError(t, err)

				meta := appcontainer.SignerImageArchMetadata{Arch: arch, Tag: repo + ":staging-v1.2.3-" + arch, Digest: digest.String(), Ref: repo + "@" + digest.String()}
				body, err := json.Marshal(meta)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile("image-arch-"+arch+"/image-"+arch+".json", body, 0o600))
			}

			var gated []string

			input := appcontainer.SignerImageAssembleInput{
				AuthFile: "auth.json", SourceSHA: strings.Repeat("b", 40), ServerURL: server.URL, Repository: "org/repo", TagName: "staging-v1.2.3", RetryAttempts: 1,
				CacheTagPrefix: "cache-arch-core",
				ManifestOutput: "dist/index.json",
				Evidence: &appcontainer.StagedLayoutEvidence{
					Trivy: &fakeImageEvidenceTrivy{}, Stderr: io.Discard,
					Syft:                &fakeImageEvidenceSyft{noOutput: blocked == "sbom"},
					TrivyOutputTemplate: "dist/trivy-{arch}.json",
					SBOMOutputTemplate:  "dist/inventory-{arch}.json",
					IndexSBOMOutput:     "dist/image-sbom.cyclonedx.json",
					SBOMURLPrefix:       "https://forge.example/org/repo/releases/download/v1.2.3",
					Gate: func(_ context.Context, _, platform string) error {
						gated = append(gated, platform)
						if platform == blocked {
							return errs.ErrValidation
						}

						return nil
					},
				},
			}

			_, err := appcontainer.PublishAndAssembleSignerImage(t.Context(), ociregistry.WithAuthFile("auth.json"), fakeoutputsink.New(t), nil, io.Discard, input)

			if blocked != "sbom" {
				require.Equal(t, []string{"linux/amd64", "linux/arm64"}, gated)
				require.FileExists(t, "dist/inventory-amd64.json")
				require.FileExists(t, "dist/inventory-arm64.json")
			}

			if blocked != "sbom" {
				require.FileExists(t, "dist/trivy-amd64.json")
				require.FileExists(t, "dist/trivy-arm64.json")
			}

			if blocked != "" {
				require.Error(t, err)
				require.Zero(t, writes.Load())
				require.NoFileExists(t, "dist/image-sbom.cyclonedx.json")

				return
			}

			require.NoError(t, err)
			require.Positive(t, writes.Load())

			for _, arch := range []string{"amd64", "arm64"} {
				digest, err := ociregistry.NewAnonymous().ResolveDigest(t.Context(), repo+":cache-arch-core-"+arch)
				require.NoError(t, err)
				child, err := ociregistry.NewAnonymous().InspectLayout(t.Context(), "image-arch-"+arch+"/layout")
				require.NoError(t, err)
				require.Equal(t, child.Digest, digest)
			}
		})
	}
}

func TestArchitectureCacheReuseStagesImmutableBytesWithoutBuildingOrWriting(t *testing.T) {
	for _, scenario := range []string{"warm", "missing", "stale", "wrong architecture"} {
		t.Run(scenario, func(t *testing.T) {
			t.Chdir(t.TempDir())

			var writes atomic.Int32

			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet && r.Method != http.MethodHead {
					writes.Add(1)
				}

				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			repo := strings.TrimPrefix(server.URL, "http://") + "/org/image"
			client := ociregistry.NewAnonymous()

			arch := "amd64"
			if scenario == "wrong architecture" {
				arch = "arm64"
			}

			_, _ = stagedLayout(t, t.Context(), "cached", repo+":cache-arch-core-amd64", arch)
			if scenario != "missing" {
				_, err := client.PushLayout(t.Context(), "cached", repo+":cache-arch-core-amd64")
				require.NoError(t, err)
			}

			writes.Store(0)

			required := "org.opencontainers.image.revision=" + strings.Repeat("b", 40)
			if scenario == "stale" {
				required = "org.example.content=new"
			}

			tool := &stagingSignerTool{t: t}
			meta, err := appcontainer.StageSignerImageArch(t.Context(), tool, client, io.Discard, appcontainer.SignerImageBuildArchInput{
				Arch: "amd64", HostArch: "amd64", SourceSHA: strings.Repeat("b", 40), ServerURL: server.URL, Repository: "org/image",
				CacheTag: "cache-arch-core-amd64", CacheLabels: []string{required},
			})
			require.NoError(t, err)
			require.NotNil(t, meta)
			require.Zero(t, writes.Load())

			if scenario == "warm" {
				require.Empty(t, tool.events)
				cached, err := client.InspectLayout(t.Context(), "cached")
				require.NoError(t, err)
				require.Equal(t, cached.Digest, meta.Digest)
			} else {
				require.Equal(t, []string{"build", "export"}, tool.events)
			}
		})
	}
}

func TestArchitectureCachePartialReuseKeepsWarmSiblingThroughAssembly(t *testing.T) {
	t.Chdir(t.TempDir())

	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	repo := strings.TrimPrefix(server.URL, "http://") + "/org/image"
	client := ociregistry.NewAnonymous()
	_, _ = stagedLayout(t, t.Context(), "cached", repo+":cache-arch-core-amd64", "amd64")
	warmDigest, err := client.PushLayout(t.Context(), "cached", repo+":cache-arch-core-amd64")
	require.NoError(t, err)

	for _, arch := range []string{"amd64", "arm64"} {
		tool := &stagingSignerTool{t: t}
		meta, stageErr := appcontainer.StageSignerImageArch(t.Context(), tool, client, io.Discard, appcontainer.SignerImageBuildArchInput{
			Arch: arch, HostArch: arch, SourceSHA: strings.Repeat("b", 40), ServerURL: server.URL, Repository: "org/image", TagName: "staging-v1.2.3",
			CacheTag: "cache-arch-core-" + arch, CacheLabels: []string{"org.opencontainers.image.revision=" + strings.Repeat("b", 40)},
		})
		require.NoError(t, stageErr)

		if arch == "amd64" {
			require.Empty(t, tool.events)
			require.Equal(t, warmDigest, meta.Digest)
		} else {
			require.Equal(t, []string{"build", "export"}, tool.events)
		}
	}

	require.NoError(t, os.WriteFile("auth.json", []byte(`{"auths":{}}`), 0o600))
	meta, err := appcontainer.PublishAndAssembleSignerImage(t.Context(), ociregistry.WithAuthFile("auth.json"), fakeoutputsink.New(t), nil, io.Discard, appcontainer.SignerImageAssembleInput{
		AuthFile: "auth.json", SourceSHA: strings.Repeat("b", 40), ServerURL: server.URL, Repository: "org/image", TagName: "staging-v1.2.3", RetryAttempts: 1,
		CacheTagPrefix: "cache-arch-core", Evidence: &appcontainer.StagedLayoutEvidence{
			Trivy: &fakeImageEvidenceTrivy{}, Syft: &fakeImageEvidenceSyft{}, Stderr: io.Discard,
			TrivyOutputTemplate: "dist/trivy-{arch}.json", SBOMOutputTemplate: "dist/inventory-{arch}.json",
			Gate: func(context.Context, string, string) error { return nil },
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, meta.Digest)
	served, err := client.ResolveDigest(t.Context(), repo+":cache-arch-core-amd64")
	require.NoError(t, err)
	require.Equal(t, warmDigest, served)
	require.FileExists(t, "dist/inventory-arm64.json")
}

func stagedLayout(t *testing.T, ctx context.Context, directory, imageRef, arch string) (appcontainer.LayoutRecordInput, v1.Image) {
	t.Helper()

	image, err := mutate.AppendLayers(empty.Image, static.NewLayer([]byte("fixture compressed layer"), types.OCILayer))
	require.NoError(t, err)
	config, err := image.ConfigFile()
	require.NoError(t, err)

	config.OS, config.Architecture = "linux", arch
	config.Config.Labels = map[string]string{"org.opencontainers.image.revision": strings.Repeat("b", 40)}
	image, err = mutate.ConfigFile(image, config)
	require.NoError(t, err)
	path, err := layout.Write(directory, empty.Index)
	require.NoError(t, err)
	require.NoError(t, path.AppendImage(image))

	in := appcontainer.LayoutRecordInput{Directory: directory, ImageRef: imageRef, Platform: "linux/" + arch, SourceSHA: strings.Repeat("b", 40)}
	_, err = appcontainer.RecordImageLayout(ctx, ociregistry.NewAnonymous(), in)
	require.NoError(t, err)

	return in, image
}

func TestLayoutPublicationRefusesAnyBadSiblingBeforeRegistryIO(t *testing.T) {
	t.Parallel()

	for _, defect := range []string{"layer", "config", "manifest", "source", "platform", "image source", "image platform", "oversized file", "digest", "destination", "missing record", "missing blob", "symlink", "layout version", "missing sibling", "extra sibling", "duplicate platform"} {
		t.Run(defect, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			repo := strings.TrimPrefix(server.URL, "http://") + "/org/image"
			root := t.TempDir()
			_, _ = stagedLayout(t, t.Context(), filepath.Join(root, "amd64"), repo, "amd64")
			second, image := stagedLayout(t, t.Context(), filepath.Join(root, "arm64"), repo, "arm64")
			manifest, err := image.Manifest()
			require.NoError(t, err)

			switch defect {
			case "layer", "config", "manifest", "missing blob", "symlink":
				digest := manifest.Layers[0].Digest
				if defect == "config" {
					digest = manifest.Config.Digest
				}

				if defect == "manifest" {
					digest, err = image.Digest()
					require.NoError(t, err)
				}

				path := filepath.Join(second.Directory, "blobs", "sha256", digest.Hex)
				require.NoError(t, os.Remove(path))

				if defect == "symlink" {
					require.NoError(t, os.Symlink(filepath.Join(root, "amd64", "index.json"), path))
				} else if defect != "missing blob" {
					require.NoError(t, os.WriteFile(path, []byte("corrupt"), 0o600))
				}
			case "missing record":
				require.NoError(t, os.Remove(filepath.Join(second.Directory, "forge-ci-image.json")))
			case "layout version":
				require.NoError(t, os.WriteFile(filepath.Join(second.Directory, "oci-layout"), []byte(`{"imageLayoutVersion":"bad"}`), 0o600))
			case "missing sibling":
				require.NoError(t, os.RemoveAll(second.Directory))
			case "extra sibling":
				require.NoError(t, os.Mkdir(filepath.Join(root, "extra"), 0o700))
			case "oversized file":
				file, createErr := os.Create(filepath.Join(second.Directory, "oversized"))
				require.NoError(t, createErr)
				// Sparse fixture: tests the bound without allocating gigabytes.
				require.NoError(t, file.Truncate(artifact.MaxFileBytes+1))
				require.NoError(t, file.Close())
			default:
				path := filepath.Join(second.Directory, "forge-ci-image.json")
				body, readErr := os.ReadFile(path)
				require.NoError(t, readErr)

				var record appcontainer.ImageLayoutRecord
				require.NoError(t, json.Unmarshal(body, &record))

				switch defect {
				case "source":
					record.SourceSHA = strings.Repeat("c", 40)
				case "platform":
					record.Platform = "linux/s390x"
				case "duplicate platform":
					record.Platform = "linux/amd64"
				case "digest":
					record.Digest = "sha256:" + strings.Repeat("0", 64)
				case "destination":
					record.ImageRef = "registry.example/other/image"
				case "image source", "image platform":
					config, configErr := image.ConfigFile()
					require.NoError(t, configErr)

					if defect == "image source" {
						config.Config.Labels["org.opencontainers.image.revision"] = strings.Repeat("c", 40)
					} else {
						config.Architecture = "s390x"
					}

					changed, mutateErr := mutate.ConfigFile(image, config)
					require.NoError(t, mutateErr)

					layoutPath, layoutErr := layout.Write(second.Directory, empty.Index)
					require.NoError(t, layoutErr)
					require.NoError(t, layoutPath.AppendImage(changed))
					digest, digestErr := changed.Digest()
					require.NoError(t, digestErr)

					record.Digest = digest.String()
				}

				body, err = json.Marshal(record)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, body, 0o600))
			}

			inputs, err := appcontainer.LayoutSetInputs(root, repo, second.SourceSHA, []string{"linux/amd64", "linux/arm64"})
			if err == nil {
				_, err = appcontainer.PublishImageLayouts(t.Context(), ociregistry.NewAnonymous(), io.Discard, inputs, 1, 0)
			}

			require.Error(t, err)
			require.Zero(t, requests.Load(), "an invalid later sibling must prevent even the first registry request")
		})
	}
}

func TestLayoutPublicationPreservesEveryStagedDigest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	repo := strings.TrimPrefix(server.URL, "http://") + "/org/image"
	root := t.TempDir()
	want := map[string]string{}

	for _, arch := range []string{"amd64", "arm64"} {
		_, image := stagedLayout(t, t.Context(), filepath.Join(root, arch), repo, arch)
		digest, err := image.Digest()
		require.NoError(t, err)

		want["linux/"+arch] = digest.String()
	}

	inputs, err := appcontainer.LayoutSetInputs(root, repo, strings.Repeat("b", 40), []string{"linux/amd64", "linux/arm64"})
	require.NoError(t, err)
	got, err := appcontainer.PublishImageLayouts(t.Context(), ociregistry.NewAnonymous(), io.Discard, inputs, 1, 0)
	require.NoError(t, err)
	require.Equal(t, want, got)

	for _, in := range inputs {
		image, err := ociregistry.NewAnonymous().InspectLayout(t.Context(), in.Directory)
		require.NoError(t, err)
		digest, err := ociregistry.NewAnonymous().ResolveDigest(t.Context(), repo+"@"+image.Digest)
		require.NoError(t, err)
		require.Equal(t, image.Digest, digest)
	}
}

func TestOCIStagingNeverPublishesAndRemovesFailedScan(t *testing.T) {
	// Scan reports use cwd, so each case owns it and cannot run in parallel.
	for _, failedScan := range []bool{false, true} {
		t.Run(map[bool]string{false: "pass", true: "refuse"}[failedScan], func(t *testing.T) {
			t.Chdir(t.TempDir())

			builder, pusher, scanner := &fakeBuilder{}, &fakePusher{}, &fakeTrivy{}
			if failedScan {
				scanner.failure = os.ErrPermission
			}

			in := pushByDigestInput()
			in.Mode, in.OutputDir, in.CachePush = container.BuildModeOCI, filepath.Join(t.TempDir(), "layout"), true
			sink := fakeoutputsink.New(t)

			digest, err := appcontainer.BuildAndScan(t.Context(), builder, pusher, scanner, sink, io.Discard, io.Discard, output.Annotator{}, appcontainer.BuildAndScanInput{Build: in})
			if failedScan {
				require.ErrorIs(t, err, os.ErrPermission)
				require.NoDirExists(t, in.OutputDir)
			} else {
				require.NoError(t, err)
				require.DirExists(t, in.OutputDir)
				_, err = appcontainer.BuildImage(t.Context(), builder, pusher, sink, io.Discard, in)
				require.ErrorIs(t, err, errs.ErrUsage, "an existing artifact must not be overwritten")
			}

			require.Empty(t, digest)
			require.Empty(t, sink.Keys())
			require.Zero(t, pusher.calls)
			require.False(t, builder.got.CachePush)
			require.Len(t, scanner.runs, 1)
		})
	}
}

func TestOCIStagingDisablesCacheWritesWithoutScanning(t *testing.T) {
	t.Parallel()

	builder, pusher := &fakeBuilder{}, &fakePusher{}
	in := pushByDigestInput()
	in.Mode, in.OutputDir, in.CachePush = container.BuildModeOCI, filepath.Join(t.TempDir(), "layout"), true
	_, err := appcontainer.BuildImage(t.Context(), builder, pusher, nil, io.Discard, in)
	require.NoError(t, err)
	require.False(t, builder.got.CachePush)
	require.Zero(t, pusher.calls)
}
