// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/kaniko"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/stretchr/testify/require"
)

// The builder is inert, but identity uses the real adapter's executable hash.
type identifiedStagingTool struct {
	*stagingSignerTool

	executable string
}

func (tool identifiedStagingTool) Identity() (domaincontainer.BuilderIdentity, error) {
	return (&kaniko.Adapter{Executor: tool.executable}).Identity()
}

func TestPreparedInputsPreservePublicBuildsWithoutRegistryAccess(t *testing.T) {
	t.Chdir(t.TempDir())
	// A closed registry makes an accidental fetch or ambient-auth lookup fail.
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	server.Close()
	input := appcontainer.SignerImageBuildArchInput{
		Name: "image", Arch: "amd64", HostArch: "amd64", SourceSHA: strings.Repeat("b", 40),
		ServerURL: server.URL, Repository: "owner/public", MetadataDir: "staged",
		BuildArgs: []string{"PUBLIC_OPTION=unchanged"},
	}
	client := ociregistry.NewAnonymous()
	require.NoError(t, appcontainer.FetchReleaseBuildInputs(t.Context(), client, io.Discard, input, nil, "inputs"))
	tool := &stagingSignerTool{t: t}
	metadata, err := appcontainer.StageReleaseBuildWithInputs(t.Context(), tool, nil, client, io.Discard, input, nil, "inputs")
	require.NoError(t, err)
	require.NotEmpty(t, metadata.Digest)
	require.Equal(t, []string{"build", "export"}, tool.events)
	require.Equal(t, input.BuildArgs, tool.buildReqs[0].BuildArgs)
	require.Empty(t, tool.pushes)
}

func TestPreparedInputPolicyMismatchRefusesBeforeBuilder(t *testing.T) {
	for _, record := range []string{
		`{"parents":null,"sourceSHA":"different-source","arch":"amd64","cacheHit":false}`,
		`{"parents":null,"sourceSHA":"` + strings.Repeat("b", 40) + `","arch":"arm64","cacheHit":false}`,
		`{"parents":null,"sourceSHA":"` + strings.Repeat("b", 40) + `","arch":"amd64","cacheHit":true}`,
	} {
		t.Run(record, func(t *testing.T) {
			t.Chdir(t.TempDir())
			require.NoError(t, os.Mkdir("inputs", 0o700))
			require.NoError(t, os.WriteFile("inputs/inputs.json", []byte(record), 0o600))

			input := appcontainer.SignerImageBuildArchInput{
				Name: "image", Arch: "amd64", HostArch: "amd64", SourceSHA: strings.Repeat("b", 40),
				ServerURL: "https://forge.example", Repository: "owner/public", MetadataDir: "staged",
			}
			tool := &stagingSignerTool{t: t}
			_, err := appcontainer.StageReleaseBuildWithInputs(t.Context(), tool, nil, ociregistry.NewAnonymous(), io.Discard, input, nil, "inputs")
			require.Error(t, err)
			require.Empty(t, tool.events)
		})
	}
}

func TestPreparedReleaseBuildRunsOfflineForColdAndWarmCaches(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "warm"}[warm], func(t *testing.T) {
			t.Chdir(t.TempDir())

			server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
			t.Cleanup(server.Close)
			repository := strings.TrimPrefix(server.URL, "http://") + "/owner/private"
			client := ociregistry.NewAnonymous()
			_, _ = stagedLayout(t, t.Context(), "parent", repository+":parent", "amd64")
			digest, err := client.PushLayout(t.Context(), "parent", repository+":parent")
			require.NoError(t, err)

			parent := repository + "@" + digest
			if warm {
				_, err = client.PushLayout(t.Context(), "parent", repository+":cache")
				require.NoError(t, err)
			}

			input := appcontainer.SignerImageBuildArchInput{
				Name: "image", Arch: "amd64", HostArch: "amd64", SourceSHA: strings.Repeat("b", 40),
				ServerURL: server.URL, Repository: "owner/private", ImageRepository: repository,
				CacheTag: "cache", CacheLabels: []string{"org.opencontainers.image.revision=" + strings.Repeat("b", 40)},
				BuildArgs: []string{"BASE_IMAGE=" + parent}, MetadataDir: "staged",
			}
			parents := map[string]string{"BASE_IMAGE": parent}
			require.NoError(t, appcontainer.FetchReleaseBuildInputs(t.Context(), client, io.Discard, input, parents, "inputs"))
			server.Close() // Build phase cannot pull a missing private input.

			executable := filepath.Join(t.TempDir(), "executor")
			require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nexit 99\n"), 0o700))
			tool := identifiedStagingTool{stagingSignerTool: &stagingSignerTool{t: t}, executable: executable}
			identity, err := tool.Identity()
			require.NoError(t, err)

			input.BuilderMaterialID = identity.MaterialID()
			loader := &registryInputFixture{}

			require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nexit 98\n"), 0o700))
			_, err = appcontainer.StageReleaseBuildWithInputs(t.Context(), tool, loader, ociregistry.NewAnonymous(), io.Discard, input, parents, "inputs")
			require.ErrorIs(t, err, errs.ErrValidation)
			require.Empty(t, tool.events, "identity mismatch must precede cache use and parent import")
			require.Zero(t, loader.loads)
			_, err = appcontainer.StageSignerImageArch(t.Context(), tool, ociregistry.NewAnonymous(), io.Discard, input)
			require.ErrorIs(t, err, errs.ErrValidation)
			require.Empty(t, tool.events)
			require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nexit 99\n"), 0o700))
			metadata, err := appcontainer.StageReleaseBuildWithInputs(t.Context(), tool, loader, ociregistry.NewAnonymous(), io.Discard, input, parents, "inputs")
			require.NoError(t, err)
			require.NotEmpty(t, metadata.Digest)

			if warm {
				require.Empty(t, tool.events)
				require.Zero(t, loader.loads)
			} else {
				require.Equal(t, []string{"build", "export"}, tool.events)
				require.Equal(t, 1, loader.loads)
				require.Equal(t, []string{"BASE_IMAGE=localhost/forge-ci-input:" + strings.TrimPrefix(digest, "sha256:")}, tool.buildReqs[0].BuildArgs)
			}

			input.AuthFile = "must-not-reach-builder"
			_, err = appcontainer.StageReleaseBuildWithInputs(t.Context(), tool, loader, client, io.Discard, input, parents, "inputs")
			require.ErrorContains(t, err, "must not receive registry credentials")
		})
	}
}
