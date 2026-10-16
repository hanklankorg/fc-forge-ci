// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

func TestSignerAssemblyUsesAuthenticatedRegistryWithoutBuildah(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir())

	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "publisher" || password != "fixture-password" {
			writer.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			writer.WriteHeader(http.StatusUnauthorized)

			return
		}

		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "http://")
	auth := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, host, base64.StdEncoding.EncodeToString([]byte("publisher:fixture-password")))
	require.NoError(t, os.WriteFile("auth.json", []byte(auth), 0o600))

	publisher := ociregistry.WithAuthFile("auth.json")
	repository := host + "/org/repo-signer"

	want := make(map[string]string)

	for _, arch := range []string{"amd64", "arm64"} {
		image, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: "linux", Architecture: arch})
		require.NoError(t, err)

		directory := t.TempDir()
		path, err := layout.Write(directory, empty.Index)
		require.NoError(t, err)
		require.NoError(t, path.AppendImage(image))

		tag := repository + ":signer-" + strings.Repeat("b", 40) + "-" + arch
		digest, err := publisher.PushLayout(t.Context(), directory, tag)
		require.NoError(t, err)

		want[arch] = digest
		writeSignerArchMetadata(t, arch, repository, strings.TrimPrefix(digest, "sha256:"))
	}

	sink := fakeoutputsink.New(t)
	metadata, err := appcontainer.AssembleSignerImageManifest(t.Context(), publisher, sink, nil, io.Discard, appcontainer.SignerImageAssembleInput{
		AuthFile: "auth.json", SourceSHA: strings.Repeat("b", 40), ServerURL: "https://source.example",
		Repository: "org/repo", ImageRepository: repository, TagPrefix: "signer-", Name: "signer-image", RetryAttempts: 1,
	})
	require.NoError(t, err)

	raw, err := publisher.Manifest(t.Context(), metadata.Tag)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), metadata.Digest)
	require.Equal(t, metadata.Ref, sink.Single("image-ref"))

	var index v1.IndexManifest
	require.NoError(t, json.Unmarshal(raw, &index))
	require.Len(t, index.Manifests, 2)

	for _, child := range index.Manifests {
		require.NotNil(t, child.Platform)
		require.Equal(t, "linux", child.Platform.OS)
		require.Equal(t, want[child.Platform.Architecture], child.Digest.String())
		delete(want, child.Platform.Architecture)
	}

	require.Empty(t, want)
	_, _, err = publisher.ImagePlatform(t.Context(), metadata.Ref)
	require.ErrorIs(t, err, errs.ErrValidation, "an index cannot masquerade as one architecture artifact")
}
