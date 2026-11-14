// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/retry"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// Restore only an externally acknowledged deleted fixture, never an unknown
// package found by name. Retention is explicit and lasts until batch cleanup.
func TestGitHubSourceImageRestoreForBatch(t *testing.T) {
	RegisterLiveScenario(t)

	if os.Getenv(GitHubContextEnv) == "" {
		t.Skip("admitted hosted context required")
	}

	if os.Getenv("FORGE_CI_LIVE_IMAGE_BATCH_RETAIN_ACK") != "retain-exact-restored-package-until-batch-end" {
		t.Fatal("explicit batch retention acknowledgement required")
	}

	orgID, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_ID"), 10, 64)
	if err != nil || orgID <= 0 {
		t.Fatal("explicit organization required")
	}

	ackID, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_IMAGE_ACK_PACKAGE_ID"), 10, 64)
	if err != nil || ackID <= 0 {
		t.Fatal("explicit deleted package ID required")
	}

	repoID, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_ENGINE_REPOSITORY_ID"), 10, 64)
	if err != nil || repoID <= 0 {
		t.Fatal("explicit source repository required")
	}

	file, err := os.Open(os.Getenv("FORGE_CI_LIVE_IMAGE_RESUME_RECEIPT"))
	if err != nil {
		t.Fatal(err)
	}

	body, err := io.ReadAll(io.LimitReader(file, 65537))
	_ = file.Close()

	if err != nil || len(body) > 65536 {
		t.Fatal("receipt unavailable or oversized")
	}

	var receipt struct {
		Package   GitHubPackageIdentity `json:"package"`
		Reference string                `json:"reference"`
		Digest    string                `json:"served_manifest_digest"`
		Source    string                `json:"source"`
		Version   string                `json:"version"`
		Platform  string                `json:"platform"`
		Layers    bool                  `json:"layer_bytes_preserved"`
	}
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatal("invalid trusted fixture receipt")
	}

	trial := OpenGitHubTrial(t, orgID)

	repo := os.Getenv("FORGE_CI_LIVE_ENGINE_REPOSITORY")
	if len(os.Getenv("FORGE_CI_LIVE_IMAGE_SOURCE")) != 64 {
		t.Fatal("exact source SHA256 required")
	}

	expectedName := repo + "/nanolinter-ci-rust-" + os.Getenv("FORGE_CI_LIVE_IMAGE_SOURCE")[:12]
	if trial.org != os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_LOGIN") || !strings.HasPrefix(repo, trial.prefix) || receipt.Package.Organization != trial.org || receipt.Package.Name != expectedName || receipt.Package.ID != ackID || receipt.Package.RepositoryID != repoID || receipt.Source != os.Getenv("FORGE_CI_LIVE_IMAGE_SOURCE") || receipt.Digest != os.Getenv("FORGE_CI_LIVE_IMAGE_EXPECTED_DIGEST") || receipt.Platform != "linux/amd64" || !receipt.Layers {
		t.Fatal("exact restore receipt acknowledgement mismatch")
	}

	repository, _, err := trial.api.Repositories.Get(t.Context(), trial.org, repo)
	if err != nil || repository.GetID() != repoID || repository.GetPrivate() {
		t.Fatal("retained public repository mismatch")
	}

	current, err := trial.ReadContainerPackageIdentity(t.Context(), expectedName, repoID)
	if errors.Is(err, os.ErrNotExist) {
		t.Logf("exact acknowledged package%d GET404; requesting documented restore for %s", ackID, expectedName)

		response, restoreErr := trial.api.Organizations.RestorePackage(t.Context(), trial.org, "container", expectedName)
		if restoreErr != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}

			t.Fatalf("documented exact fixture restore unavailable HTTP%d; no upload or visibility changes attempted", status)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		current, err = retry.Do(ctx, nil, 15, 2*time.Second, func() (GitHubPackageIdentity, error) {
			observed, readErr := trial.ReadContainerPackageIdentity(ctx, expectedName, repoID)
			if readErr == nil || errors.Is(readErr, os.ErrNotExist) {
				return observed, readErr
			}

			return observed, retry.Permanent(readErr)
		}, retry.WithBackoff(retry.Constant))
	}

	if err != nil || current != receipt.Package {
		t.Fatal("restored package ID or repository differs; refusing reuse")
	}

	pkg, _, err := trial.api.Organizations.GetPackage(t.Context(), trial.org, "container", expectedName)
	if err != nil {
		t.Fatal("restored metadata unavailable")
	}

	observation := map[string]any{"package": current, "visibility": pkg.GetVisibility(), "reference": receipt.Reference, "expected_manifest_digest": receipt.Digest, "source": receipt.Source, "version": receipt.Version, "platform": receipt.Platform, "status": "retained until acknowledged batch cleanup"}
	if err := writeSnapshotEvidence(snapshotEvidenceRoot(), "restored-source-image-batch.json", mustSourceImageJSON(t, observation)); err != nil {
		t.Fatal(err)
	}

	t.Logf("restored exact fixture retained: id=%d repository=%d visibility=%s", current.ID, current.RepositoryID, pkg.GetVisibility())

	if pkg.GetVisibility() != "public" {
		t.Skip("exact restored package retained but private; one-time settings UI needed before any PR reuse")
	}

	ref, err := name.NewDigest(receipt.Reference)
	if err != nil || ref.DigestStr() != receipt.Digest || ref.Context().Name() != "ghcr.io/"+strings.ToLower(trial.org)+"/"+expectedName {
		t.Fatal("receipt digest coordinate mismatch")
	}

	image, err := remote.Image(ref, remote.WithContext(t.Context()), remote.WithAuth(authn.Anonymous))
	if err != nil {
		t.Fatal("restored public image anonymous access failed")
	}

	digest, err := image.Digest()
	if err != nil || digest.String() != receipt.Digest {
		t.Fatal("restored manifest changed")
	}

	config, err := image.ConfigFile()
	if err != nil || config.OS != "linux" || config.Architecture != "amd64" || config.Config.Labels["org.opencontainers.image.revision"] != receipt.Source || config.Config.Labels["org.opencontainers.image.version"] != receipt.Version {
		t.Fatal("restored source/version/platform changed")
	}

	original, err := tarball.ImageFromPath(os.Getenv("FORGE_CI_LIVE_IMAGE_ARCHIVE"), nil)
	if err != nil {
		t.Fatal(err)
	}

	a, err := original.Layers()
	if err != nil {
		t.Fatal(err)
	}

	b, err := image.Layers()
	if err != nil || len(a) != len(b) {
		t.Fatal("restored layers changed")
	}

	for index := range a {
		x, err := a[index].Digest()
		if err != nil {
			t.Fatal(err)
		}

		y, err := b[index].Digest()
		if err != nil || x != y {
			t.Fatal("restored layer digest changed")
		}
	}

	observation["public_anonymous_pull"] = true
	observation["layer_bytes_preserved"] = true

	observation["status"] = "verified public fixture retained for batch reuse; cleanup pending"
	if err := writeSnapshotEvidence(snapshotEvidenceRoot(), "restored-source-image-batch.json", mustSourceImageJSON(t, observation)); err != nil {
		t.Fatal(err)
	}

	t.Logf("batch CI image verified public: id=%d repository=%d digest=%s; explicit retention, no deletion", current.ID, current.RepositoryID, digest)
}

func mustSourceImageJSON(t *testing.T, value any) []byte {
	t.Helper()

	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	return body
}
