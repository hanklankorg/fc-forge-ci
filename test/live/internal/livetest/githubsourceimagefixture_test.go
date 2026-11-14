// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sourceImageVersion struct {
	ID     int64  `json:"id"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}

type sourceImageReceipt struct {
	Package   GitHubPackageIdentity `json:"package"`
	Digest    string                `json:"expected_manifest_digest"`
	Reference string                `json:"reference"`
	Config    string                `json:"original_image_config"`
	Source    string                `json:"source"`
	Versions  []sourceImageVersion  `json:"versions"`
}

func sourceImageMatches(config *v1.ConfigFile, hash v1.Hash, source, configID, flavor string) bool {
	return config != nil && hash.Hex == configID && config.OS == "linux" && config.Architecture == "amd64" && config.Config.Labels["org.opencontainers.image.revision"] == source && config.Config.Labels["org.nanolinter.flavor"] == flavor
}

func sourceImageLayersMatch(original, served v1.Image) bool {
	a, err := original.Layers()
	if err != nil {
		return false
	}

	b, err := served.Layers()
	if err != nil || len(a) != len(b) {
		return false
	}

	for index, layer := range a {
		x, err := layer.Digest()
		if err != nil {
			return false
		}

		y, err := b[index].Digest()
		if err != nil || x != y {
			return false
		}
	}

	return true
}

// One remote.Write of this single-platform image owns one manifest version,
// not every version a later reader happens to find under the same package ID.
func sourceImageVersionsValid(versions []sourceImageVersion, digest string) bool {
	hash, err := v1.NewHash(digest)

	return err == nil && hash.Algorithm == "sha256" && len(versions) == 1 && versions[0].ID > 0 && versions[0].Digest == digest && versions[0].State == "active"
}

func sourceImageReceiptMatches(receipt, expected sourceImageReceipt) bool {
	return receipt.Package == expected.Package && receipt.Digest == expected.Digest && receipt.Reference == expected.Reference && receipt.Config == expected.Config && receipt.Source == expected.Source && sourceImageVersionsValid(receipt.Versions, expected.Digest)
}

func (trial *GitHubTrial) readSourceImageVersions(ctx context.Context, identity GitHubPackageIdentity, digest string) ([]sourceImageVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if identity.Organization != trial.org || identity.ID <= 0 {
		return nil, errPackageDeletionUnacknowledged
	}

	current, err := trial.ReadContainerPackageIdentity(ctx, identity.Name, identity.RepositoryID)
	if err != nil {
		return nil, err
	}

	if current != identity {
		return nil, errPackageIdentityDrift
	}

	var versions []sourceImageVersion
	// Package deletion also removes restorable versions: do not hide them by
	// querying only active versions and then claiming the whole package.
	for _, state := range []string{"active", "deleted"} {
		options := &github.PackageListOptions{State: &state, Page: 1, PerPage: 100}
		for {
			page, response, err := trial.api.Organizations.PackageGetAllVersions(ctx, trial.org, "container", identity.Name, options)
			if err != nil || response == nil {
				return nil, errors.New("source image versions unavailable; refusing cleanup")
			}

			for _, version := range page {
				versions = append(versions, sourceImageVersion{ID: version.GetID(), Digest: version.GetName(), State: state})
			}

			if response.NextPage == 0 {
				break
			}

			if response.NextPage <= options.Page {
				return nil, errors.New("source image version pagination did not advance")
			}

			options.Page = response.NextPage
		}
	}

	if !sourceImageVersionsValid(versions, digest) {
		return nil, errors.New("source image version set is not the exact published manifest")
	}

	return versions, nil
}

func (trial *GitHubTrial) checkSourceImageVersions(ctx context.Context, identity GitHubPackageIdentity, acknowledged []sourceImageVersion) error {
	if len(acknowledged) != 1 || !sourceImageVersionsValid(acknowledged, acknowledged[0].Digest) {
		return errors.New("source image publication version acknowledgement required")
	}

	current, err := trial.readSourceImageVersions(ctx, identity, acknowledged[0].Digest)
	if err != nil {
		return err
	}

	if !slices.Equal(current, acknowledged) {
		return errors.New("source image version identity drift; refusing cleanup")
	}

	return nil
}

func (trial *GitHubTrial) deleteSourceImagePackage(ctx context.Context, identity GitHubPackageIdentity, versions []sourceImageVersion) error {
	if err := trial.checkSourceImageVersions(ctx, identity, versions); err != nil {
		return err
	}

	if _, err := trial.api.Organizations.DeletePackage(ctx, trial.org, "container", identity.Name); err != nil {
		return errors.New("source image package deletion failed")
	}

	_, response, err := trial.api.Organizations.GetPackage(ctx, trial.org, "container", identity.Name)
	if err == nil || response == nil || response.StatusCode != http.StatusNotFound {
		return errPackageDeletionUnconfirmed
	}

	return nil
}

func TestSourceImageArchiveAdmissionAndLayers(t *testing.T) {
	const source = "8992cf876f04862299fdb07f118f72215417b87f5e3c4f1448cb9144d421869b"

	makeLayer := func(t *testing.T, content string) v1.Layer {
		t.Helper()

		var buffer bytes.Buffer

		writer := tar.NewWriter(&buffer)
		require.NoError(t, writer.WriteHeader(&tar.Header{Name: "fixture", Mode: 0o644, Size: int64(len(content))}))
		_, err := writer.Write([]byte(content))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(buffer.Bytes())), nil
		})
		require.NoError(t, err)

		return layer
	}
	for _, flavor := range []string{"go", "rust"} {
		t.Run(flavor, func(t *testing.T) {
			config := &v1.ConfigFile{OS: "linux", Architecture: "amd64", Config: v1.Config{Labels: map[string]string{"org.opencontainers.image.revision": source, "org.nanolinter.flavor": flavor}}}
			image, err := mutate.ConfigFile(empty.Image, config)
			require.NoError(t, err)
			layer := makeLayer(t, "bounded fixture layer")
			image, err = mutate.AppendLayers(image, layer)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "image.tar")
			reference, err := name.NewTag("example.invalid/fixture:" + flavor)
			require.NoError(t, err)
			require.NoError(t, tarball.WriteToFile(path, reference, image))
			loaded, err := tarball.ImageFromPath(path, nil)
			require.NoError(t, err)
			actual, err := loaded.ConfigFile()
			require.NoError(t, err)
			hash, err := loaded.ConfigName()
			require.NoError(t, err)
			selected, err := sourceImageFlavor(flavor)
			require.NoError(t, err)
			require.True(t, sourceImageMatches(actual, hash, source, hash.Hex, selected))
			require.True(t, sourceImageLayersMatch(image, loaded))
			differentLayer := makeLayer(t, "changed fixture layer")
			different, err := mutate.AppendLayers(empty.Image, differentLayer)
			require.NoError(t, err)
			require.False(t, sourceImageLayersMatch(loaded, different))
			require.False(t, sourceImageLayersMatch(loaded, empty.Image))

			if flavor == "rust" {
				legacy, err := sourceImageFlavor("")
				require.NoError(t, err)
				require.True(t, sourceImageMatches(actual, hash, source, hash.Hex, legacy))
			}

			for _, mismatch := range []string{"architecture", "os", "source", "config", "flavor"} {
				t.Run(mismatch, func(t *testing.T) {
					changed := actual.DeepCopy()
					wantConfig := hash.Hex

					switch mismatch {
					case "architecture":
						changed.Architecture = "arm64"
					case "os":
						changed.OS = "darwin"
					case "source":
						changed.Config.Labels["org.opencontainers.image.revision"] = strings.Repeat("a", 64)
					case "config":
						wantConfig = strings.Repeat("b", 64)
					case "flavor":
						changed.Config.Labels["org.nanolinter.flavor"] = "core"
					}

					require.False(t, sourceImageMatches(changed, hash, source, wantConfig, selected))
				})
			}
		})
	}
}

func TestSourceImageReceiptAdmission(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	expected := sourceImageReceipt{
		Package: GitHubPackageIdentity{Organization: "example-org", Name: "scratch-engine/nanolinter-ci-go-8992cf876f04", ID: 77, RepositoryID: 42},
		Digest:  digest, Reference: "ghcr.io/example-org/scratch-engine/nanolinter-ci-go-8992cf876f04@" + digest,
		Config: "sha256:" + strings.Repeat("b", 64), Source: strings.Repeat("c", 64),
	}

	for _, mismatch := range []string{"none", "organization", "package-id", "repository-id", "name", "digest", "reference", "config", "source", "missing-versions", "unacknowledged-version", "unowned-digest", "extra-version"} {
		t.Run(mismatch, func(t *testing.T) {
			receipt := expected
			receipt.Versions = []sourceImageVersion{{ID: 101, Digest: digest, State: "active"}}

			switch mismatch {
			case "organization":
				receipt.Package.Organization = "other"
			case "package-id":
				receipt.Package.ID++
			case "repository-id":
				receipt.Package.RepositoryID++
			case "name":
				receipt.Package.Name = "scratch-engine/another"
			case "digest":
				receipt.Digest = "sha256:" + strings.Repeat("d", 64)
			case "reference":
				receipt.Reference = "ghcr.io/another/image@" + digest
			case "config":
				receipt.Config = "sha256:" + strings.Repeat("d", 64)
			case "source":
				receipt.Source = strings.Repeat("d", 64)
			case "missing-versions":
				receipt.Versions = nil
			case "unacknowledged-version":
				receipt.Versions[0].ID = 0
			case "unowned-digest":
				receipt.Versions[0].Digest = "sha256:" + strings.Repeat("d", 64)
			case "extra-version":
				receipt.Versions = append(receipt.Versions, sourceImageVersion{ID: 102, Digest: digest})
			}

			encoded, err := json.Marshal(receipt)
			require.NoError(t, err)

			var decoded sourceImageReceipt
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			require.Equal(t, mismatch == "none", sourceImageReceiptMatches(decoded, expected))
		})
	}
}

func TestSourceImageCleanupRejectsChangedVersions(t *testing.T) {
	const packageName = "scratch-engine/nanolinter-ci-go-8992cf876f04"

	digest := "sha256:" + strings.Repeat("a", 64)
	ack := GitHubPackageIdentity{Organization: "example-org", Name: packageName, ID: 77, RepositoryID: 42}
	versions := []sourceImageVersion{{ID: 101, Digest: digest, State: "active"}}

	for _, scenario := range []string{"owned", "added-version", "replaced-version", "digest-drift", "deleted-version", "foreign-deleted", "duplicate-version", "later-page", "page-error", "cycle-page", "unavailable", "version-404", "deleted-unavailable", "malformed", "invalid-version", "empty-receipt", "package-drift", "repository-drift", "name-drift", "wrong-organization", "unacknowledged", "repository-unacknowledged", "delete-error", "absence-unconfirmed"} {
		t.Run(scenario, func(t *testing.T) {
			deletes, pages := 0, 0

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.True(t, strings.HasPrefix(request.URL.EscapedPath(), "/orgs/example-org/packages/container/scratch-engine%2Fnanolinter-ci-go-8992cf876f04"))

				if request.Method == http.MethodDelete {
					deletes++

					if scenario == "delete-error" {
						writer.WriteHeader(http.StatusServiceUnavailable)

						return
					}

					writer.WriteHeader(http.StatusNoContent)

					return
				}

				assert.Equal(t, http.MethodGet, request.Method)

				if strings.HasSuffix(request.URL.Path, "/versions") {
					state := request.URL.Query().Get("state")
					if state == "deleted" {
						if scenario == "deleted-unavailable" {
							writer.WriteHeader(http.StatusServiceUnavailable)

							return
						}

						deleted := []map[string]any{}
						if scenario == "foreign-deleted" {
							deleted = append(deleted, map[string]any{"id": 102, "name": "sha256:" + strings.Repeat("b", 64)})
						}

						assert.NoError(t, json.NewEncoder(writer).Encode(deleted))

						return
					}

					pages++

					assert.Equal(t, "active", state)
					assert.Equal(t, "100", request.URL.Query().Get("per_page"))

					if scenario == "version-404" {
						writer.WriteHeader(http.StatusNotFound)

						return
					}

					if scenario == "unavailable" || scenario == "page-error" && request.URL.Query().Get("page") == "2" {
						writer.WriteHeader(http.StatusServiceUnavailable)

						return
					}

					if scenario == "malformed" {
						_, _ = writer.Write([]byte("not JSON"))

						return
					}

					current := []map[string]any{{"id": 101, "name": digest}}

					switch scenario {
					case "added-version":
						current = append(current, map[string]any{"id": 102, "name": "sha256:" + strings.Repeat("b", 64)})
					case "replaced-version":
						current[0]["id"] = 102
					case "digest-drift":
						current[0]["name"] = "sha256:" + strings.Repeat("b", 64)
					case "duplicate-version":
						current = append(current, current[0])
					case "deleted-version":
						current = nil
					case "invalid-version":
						current[0]["id"] = 0
					case "cycle-page":
						writer.Header().Set("Link", fmt.Sprintf("<http://%s%s?page=1>; rel=\"next\"", request.Host, request.URL.EscapedPath()))
					case "later-page", "page-error":
						if request.URL.Query().Get("page") == "2" {
							current = []map[string]any{{"id": 102, "name": "sha256:" + strings.Repeat("b", 64)}}
						} else {
							writer.Header().Set("Link", fmt.Sprintf("<http://%s%s?page=2>; rel=\"next\"", request.Host, request.URL.EscapedPath()))
						}
					}

					assert.NoError(t, json.NewEncoder(writer).Encode(current))

					return
				}

				if deletes > 0 && scenario != "absence-unconfirmed" {
					writer.WriteHeader(http.StatusNotFound)

					return
				}

				pkg := map[string]any{"id": 77, "name": packageName, "repository": map[string]int64{"id": 42}}

				switch scenario {
				case "package-drift":
					pkg["id"] = 78
				case "repository-drift":
					pkg["repository"] = map[string]int64{"id": 99}
				case "name-drift":
					pkg["name"] = "another"
				}

				assert.NoError(t, json.NewEncoder(writer).Encode(pkg))
			}))
			defer server.Close()

			api, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil))
			require.NoError(t, err)

			trial := &GitHubTrial{api: api, org: "example-org"}

			expected := ack
			if scenario == "wrong-organization" {
				expected.Organization = "another"
			}

			if scenario == "unacknowledged" {
				expected.ID = 0
			}

			if scenario == "repository-unacknowledged" {
				expected.RepositoryID = 0
			}

			owned := versions
			if scenario == "empty-receipt" {
				owned = nil
			}

			err = trial.deleteSourceImagePackage(t.Context(), expected, owned)

			switch scenario {
			case "owned":
				require.NoError(t, err)
				require.Equal(t, 1, deletes)
			case "delete-error", "absence-unconfirmed":
				require.Error(t, err)
				require.Equal(t, 1, deletes)
			default:
				require.Zero(t, deletes)
				require.Error(t, err, "stale receipt must not delete a later batch")
			}

			if scenario == "later-page" || scenario == "page-error" {
				require.Equal(t, 2, pages)
			}
		})
	}
}
