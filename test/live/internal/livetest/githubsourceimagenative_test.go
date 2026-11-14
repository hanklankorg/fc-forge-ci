// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/stretchr/testify/require"
)

func TestSourceImageNativePhaseEffects(t *testing.T) {
	for _, scenario := range []string{"resume-public-go", "resume-private-go", "resume-default-rust", "version-drift", "invalid-receipt", "missing-phase", "invalid-phase", "publish-default-rust"} {
		t.Run(scenario, func(t *testing.T) {
			binary, err := os.Executable()
			require.NoError(t, err)
			out := t.TempDir()

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			command := exec.CommandContext(ctx, binary, "-test.run=^TestSourceImageNativeChild$", "-test.v")

			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "FORGE_CI_") && !strings.HasPrefix(entry, "FORGE_LAB_") {
					command.Env = append(command.Env, entry)
				}
			}

			command.Env = append(command.Env, "FORGE_CI_SOURCE_IMAGE_INERT_TEST="+scenario, "FORGE_CI_SOURCE_IMAGE_INERT_OUTPUT="+out)
			output, runErr := command.CombinedOutput()

			invalid := strings.HasPrefix(scenario, "invalid-") || scenario == "missing-phase"
			if invalid || scenario == "version-drift" {
				require.Error(t, runErr, "%s", output)
			} else {
				require.NoError(t, runErr, "%s", output)
			}

			body, err := os.ReadFile(filepath.Join(out, "requests.json"))
			require.NoError(t, err, "%s", output)

			var observed struct {
				APIReads          int32 `json:"APIReads"`
				APIMutations      int32 `json:"APIMutations"`
				RegistryReads     int32 `json:"RegistryReads"`
				RegistryMutations int32 `json:"RegistryMutations"`
				ManifestUploads   int32 `json:"ManifestUploads"`
				Unexpected        int32 `json:"Unexpected"`
				ReceiptUnchanged  bool  `json:"ReceiptUnchanged"`
			}
			require.NoError(t, json.Unmarshal(body, &observed))
			require.Zero(t, observed.APIMutations, "DELETE/repository creation/workflow mutation: %s", output)
			require.Zero(t, observed.Unexpected, "%s", output)
			require.True(t, observed.ReceiptUnchanged)
			require.NoFileExists(t, filepath.Join(out, "consumer-called"))

			if scenario == "publish-default-rust" {
				require.EqualValues(t, 1, observed.ManifestUploads)
				require.Positive(t, observed.RegistryMutations)
			} else {
				require.Zero(t, observed.ManifestUploads, "%s", output)
				require.Zero(t, observed.RegistryMutations, "%s", output)
				require.NotContains(t, string(output), "source image uploaded:")
			}

			switch {
			case invalid:
				require.Zero(t, observed.APIReads, "local refusal must precede provider reads: %s", output)
				require.Zero(t, observed.RegistryReads)
			case scenario == "version-drift":
				require.Contains(t, string(output), "version identity drift")
			case strings.Contains(scenario, "private") || scenario == "publish-default-rust":
				require.Contains(t, string(output), "PENDING_PUBLIC_CONFIRMATION")
			default:
				proof, err := os.ReadFile(filepath.Join(out, "source-image-publication.json"))
				require.NoError(t, err, "%s", output)

				var data map[string]any
				require.NoError(t, json.Unmarshal(proof, &data))
				require.Equal(t, true, data["batch_retained"])
				require.Equal(t, true, data["public_anonymous_pull"])
			}

			if !invalid && scenario != "version-drift" {
				observation, err := os.ReadFile(filepath.Join(out, "source-image-upload-observation.json"))
				require.NoError(t, err)

				var data map[string]any
				require.NoError(t, json.Unmarshal(observation, &data))

				phase := "resume"
				if scenario == "publish-default-rust" {
					phase = "publish-retain"
				}

				require.Equal(t, phase, data["phase"])
				require.Equal(t, scenario == "publish-default-rust", data["upload_acknowledged"])
			}
		})
	}
}

// This child executes the actual live entrypoint, but all SDK/registry dials
// terminate at one inert TLS server. Unknown destinations are hard-refused.
// Subprocess isolation keeps these transports away from every other test/run.
func TestSourceImageNativeChild(t *testing.T) {
	scenario := os.Getenv("FORGE_CI_SOURCE_IMAGE_INERT_TEST")
	if scenario == "" {
		t.Skip("provider-free subprocess fixture only")
	}

	out := os.Getenv("FORGE_CI_SOURCE_IMAGE_INERT_OUTPUT")

	flavor := "go"
	if strings.Contains(scenario, "rust") {
		flavor = "rust"
	}

	const source = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	packageName := "scratch-engine/nanolinter-ci-" + flavor + "-" + source[:12]

	var (
		reads, apiWrites, registryReads, registryWrites, uploads, unexpected atomic.Int32
		published, recording                                                 atomic.Bool
	)

	digest := ""
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Host == "ghcr.io" {
			if recording.Load() && (request.Method == http.MethodGet || request.Method == http.MethodHead) {
				registryReads.Add(1)
			}

			if recording.Load() && request.Method != http.MethodGet && request.Method != http.MethodHead {
				registryWrites.Add(1)
			}

			if request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/manifests/") {
				published.Store(true)

				if recording.Load() {
					uploads.Add(1)
				}
			}

			reg.ServeHTTP(writer, request)

			return
		}

		if request.Host != "api.github.com" {
			unexpected.Add(1)
			http.Error(writer, "inert destination only", http.StatusBadRequest)

			return
		}

		if request.Method != http.MethodGet {
			apiWrites.Add(1)
			http.Error(writer, "no API mutations allowed", http.StatusMethodNotAllowed)

			return
		}

		reads.Add(1)

		var value any

		switch request.URL.Path {
		case "/orgs/example-org":
			value = map[string]any{"id": 42, "login": "example-org"}
		case "/user":
			value = map[string]any{"id": 7, "login": "actor"}
		case "/repos/example-org/scratch-engine":
			value = map[string]any{"id": 24, "private": false}
		case "/orgs/example-org/packages/container/" + packageName:
			if !published.Load() {
				writer.WriteHeader(http.StatusNotFound)

				return
			}

			visibility := "public"
			if scenario == "resume-private-go" || scenario == "publish-default-rust" {
				visibility = "private"
			}

			value = map[string]any{"id": 77, "name": packageName, "visibility": visibility, "repository": map[string]int{"id": 24}}
		case "/orgs/example-org/packages/container/" + packageName + "/versions":
			value = []map[string]any{}
			if request.URL.Query().Get("state") == "active" {
				value = []map[string]any{{"id": 101, "name": digest}}
				if scenario == "version-drift" {
					value = []map[string]any{{"id": 102, "name": digest}}
				}
			}
		default:
			unexpected.Add(1)
			writer.WriteHeader(http.StatusNotFound)

			return
		}

		if err := json.NewEncoder(writer).Encode(value); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil || host != "api.github.com" && host != "ghcr.io" {
			return nil, errors.New("inert fixture refuses destination")
		}

		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "https://"))
	}}
	oldHTTP, oldRegistry := http.DefaultTransport, remote.DefaultTransport
	http.DefaultTransport, remote.DefaultTransport = transport, transport

	t.Cleanup(func() {
		http.DefaultTransport, remote.DefaultTransport = oldHTTP, oldRegistry

		transport.CloseIdleConnections()
	})

	config := &v1.ConfigFile{OS: "linux", Architecture: "amd64", Config: v1.Config{Labels: map[string]string{"org.nanolinter.flavor": flavor, "org.opencontainers.image.revision": source, "org.opencontainers.image.source": "https://example.invalid/source", "org.opencontainers.image.version": "inert-fixture"}}}
	image, err := mutate.ConfigFile(empty.Image, config)
	require.NoError(t, err)

	var payload bytes.Buffer

	tarWriter := tar.NewWriter(&payload)
	require.NoError(t, tarWriter.WriteHeader(&tar.Header{Name: "fixture", Mode: 0o644}))
	require.NoError(t, tarWriter.Close())

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload.Bytes())), nil
	})
	require.NoError(t, err)
	image, err = mutate.AppendLayers(image, layer)
	require.NoError(t, err)
	archive := filepath.Join(t.TempDir(), "fixture.tar")
	ref, err := name.NewTag("ghcr.io/example-org/" + packageName + ":source")
	require.NoError(t, err)
	require.NoError(t, tarball.WriteToFile(archive, ref, image))
	image, err = tarball.ImageFromPath(archive, nil)
	require.NoError(t, err)
	configHash, err := image.ConfigName()
	require.NoError(t, err)
	config, err = image.ConfigFile()
	require.NoError(t, err)

	config.Config.Labels["org.nanolinter.upstream-source"] = config.Config.Labels["org.opencontainers.image.source"]
	config.Config.Labels["org.opencontainers.image.source"] = "https://github.com/example-org/scratch-engine"
	mirrored, err := mutate.ConfigFile(image, config)
	require.NoError(t, err)
	hash, err := mirrored.Digest()
	require.NoError(t, err)

	digest = hash.String()

	if scenario != "publish-default-rust" {
		require.NoError(t, remote.Write(ref, mirrored, remote.WithContext(t.Context()), remote.WithAuth(authn.Anonymous), remote.WithTransport(transport)))
	}

	receipt := sourceImageReceipt{Package: GitHubPackageIdentity{Organization: "example-org", Name: packageName, ID: 77, RepositoryID: 24}, Digest: digest, Reference: ref.Context().Digest(digest).String(), Config: configHash.String(), Source: source, Versions: []sourceImageVersion{{ID: 101, Digest: digest, State: "active"}}}
	if scenario == "invalid-receipt" {
		receipt.Package.ID++
	}

	encoded, err := json.Marshal(receipt)
	require.NoError(t, err)
	original := filepath.Join(t.TempDir(), "original.json")
	require.NoError(t, os.WriteFile(original, encoded, 0o600))
	contextPath := filepath.Join(t.TempDir(), "inert-context.json")
	document := map[string]any{"version": 3, "session": strings.Repeat("b", 32), "organization": map[string]any{"id": 42, "login": "example-org"}, "actor": map[string]any{"id": 7, "login": "actor"}, "prefix": "scratch-", "token": "inert-token-not-a-credential", "fixture_manifest_file": filepath.Join(out, "unused-manifest.json"), "capabilities": []string{"repositories"}, "repositories": []any{}}
	contextBytes, err := json.Marshal(document)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(contextPath, contextBytes, 0o600))
	tools := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tools, "just"), []byte("#!/bin/sh\nprintf invoked > \"$FORGE_CI_SOURCE_IMAGE_INERT_OUTPUT/consumer-called\"\nexit 77\n"), 0o700))
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))

	for key, value := range map[string]string{GitHubContextEnv: contextPath, "FORGE_CI_LIVE_GITHUB_ORG_ID": "42", "FORGE_CI_LIVE_GITHUB_ORG_LOGIN": "example-org", "FORGE_CI_LIVE_ENGINE_REPOSITORY": "scratch-engine", "FORGE_CI_LIVE_ENGINE_REPOSITORY_ID": "24", "FORGE_CI_LIVE_IMAGE_ARCHIVE": archive, "FORGE_CI_LIVE_IMAGE_SOURCE": source, "FORGE_CI_LIVE_IMAGE_CONFIG": configHash.Hex, "FORGE_CI_LIVE_IMAGE_FLAVOR": flavor, "FORGE_CI_LIVE_IMAGE_PHASE": "resume", "FORGE_CI_LIVE_IMAGE_RETENTION_ACK": "retain-exact-package-until-public-confirmation", "FORGE_CI_LIVE_IMAGE_RESUME_RECEIPT": original, "FORGE_CI_LIVE_IMAGE_ACK_PACKAGE_ID": "77", "FORGE_CI_LIVE_IMAGE_BATCH_RETAIN_ACK": "retain-exact-restored-package-until-batch-end", "FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR": out} {
		t.Setenv(key, value)
	}

	if strings.Contains(scenario, "rust") {
		t.Setenv("FORGE_CI_LIVE_IMAGE_FLAVOR", "")
	}

	if scenario == "publish-default-rust" {
		t.Setenv("FORGE_CI_LIVE_IMAGE_PHASE", "publish-retain")
		t.Setenv("FORGE_CI_LIVE_IMAGE_BATCH_RETAIN_ACK", "")
	}

	if scenario == "missing-phase" {
		t.Setenv("FORGE_CI_LIVE_IMAGE_PHASE", "")
	}

	if scenario == "invalid-phase" {
		t.Setenv("FORGE_CI_LIVE_IMAGE_PHASE", "invalid")
	}

	reads.Store(0)
	recording.Store(true)
	t.Cleanup(func() {
		unchanged, err := os.ReadFile(original)
		observed := map[string]any{"APIReads": reads.Load(), "APIMutations": apiWrites.Load(), "RegistryReads": registryReads.Load(), "RegistryMutations": registryWrites.Load(), "ManifestUploads": uploads.Load(), "Unexpected": unexpected.Load(), "ReceiptUnchanged": err == nil && bytes.Equal(encoded, unchanged)}
		body, err := json.Marshal(observed)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(out, "requests.json"), body, 0o600))
	})
	TestGitHubSourceImageConsumerPullRequest(t)
}
