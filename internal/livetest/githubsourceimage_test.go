// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/retry"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// Publish a supplied, source-verified native tool image only under the admitted
// retained engine, then exercise the existing consumer harness before deleting
// this case's exact package. The prepared engine itself is never rebuilt here.
func TestGitHubSourceImageConsumerPullRequest(t *testing.T) {
	RegisterLiveScenario(t)
	phase, phaseErr := sourceImagePhase(os.Getenv("FORGE_CI_LIVE_IMAGE_PHASE"), os.Getenv("FORGE_CI_LIVE_IMAGE_RETENTION_ACK"))
	if phaseErr != nil {
		t.Fatal(phaseErr)
	}
	if os.Getenv(GitHubContextEnv) == "" {
		t.Skip("hosted SDK context required")
	}
	id, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_ID"), 10, 64)
	if err != nil || id <= 0 {
		t.Fatal("organization acknowledgement required")
	}
	repoID, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_ENGINE_REPOSITORY_ID"), 10, 64)
	if err != nil || repoID <= 0 {
		t.Fatal("exact retained engine ID required")
	}
	repo := os.Getenv("FORGE_CI_LIVE_ENGINE_REPOSITORY")
	archive, source, configID := os.Getenv("FORGE_CI_LIVE_IMAGE_ARCHIVE"), os.Getenv("FORGE_CI_LIVE_IMAGE_SOURCE"), os.Getenv("FORGE_CI_LIVE_IMAGE_CONFIG")
	if archive == "" || len(source) != 64 || len(configID) != 64 || repo == "" {
		t.Fatal("explicit image archive/source/config required")
	}
	trial := OpenGitHubTrial(t, id)
	if trial.org != os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_LOGIN") || !strings.HasPrefix(repo, trial.prefix) {
		t.Fatal("runtime organization/repository mismatch")
	}
	repository, _, err := trial.api.Repositories.Get(t.Context(), trial.org, repo)
	if err != nil || repository.GetID() != repoID || repository.GetPrivate() {
		t.Fatal("public retained engine identity mismatch")
	}
	image, err := tarball.ImageFromPath(archive, nil)
	if err != nil {
		t.Fatal("image archive unavailable")
	}
	config, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	configHash, err := image.ConfigName()
	if err != nil || configHash.Hex != configID || config.OS != "linux" || config.Architecture != "amd64" || config.Config.Labels["org.opencontainers.image.revision"] != source || config.Config.Labels["org.nanolinter.flavor"] != "rust" {
		t.Fatal("archive image/source/native Rust identity mismatch")
	}
	packageName := repo + "/nanolinter-ci-rust-" + source[:12]
	if phase != "resume" {
		if _, err := trial.ReadContainerPackageIdentity(t.Context(), packageName, repoID); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("publication destination already exists or is not safely readable")
		}
	}
	// Only mirror metadata changes: preserve the actual upstream source in a
	// separate label, retaining revision/version and every executable layer.
	config.Config.Labels["org.nanolinter.upstream-source"] = config.Config.Labels["org.opencontainers.image.source"]
	config.Config.Labels["org.opencontainers.image.source"] = "https://github.com/" + trial.org + "/" + repo
	mirrored, err := mutate.ConfigFile(image, config)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := name.NewTag("ghcr.io/" + strings.ToLower(trial.org) + "/" + packageName + ":source")
	if err != nil {
		t.Fatal(err)
	}
	actor, _, err := trial.api.Users.Get(t.Context(), "")
	if err != nil {
		t.Fatal("actor metadata unavailable")
	}
	options := []remote.Option{remote.WithContext(t.Context()), remote.WithAuth(authn.FromConfig(authn.AuthConfig{Username: actor.GetLogin(), Password: trial.token}))}
	expected, err := mirrored.Digest()
	if err != nil {
		t.Fatal(err)
	}
	digestReference := reference.Context().Digest(expected.String())
	retained := false
	var writeErr error
	var identity GitHubPackageIdentity
	if phase == "resume" {
		path := os.Getenv("FORGE_CI_LIVE_IMAGE_RESUME_RECEIPT")
		ackID, e := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_IMAGE_ACK_PACKAGE_ID"), 10, 64)
		if e != nil || ackID <= 0 || !filepath.IsAbs(path) {
			t.Fatal("resume requires explicit receipt and package ID")
		}
		file, e := os.Open(path)
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(io.LimitReader(file, 65537))
		_ = file.Close()
		if e != nil || len(body) > 65536 {
			t.Fatal("resume receipt unavailable or oversized")
		}
		var receipt struct {
			Package   GitHubPackageIdentity `json:"package"`
			Digest    string                `json:"expected_manifest_digest"`
			Reference string                `json:"reference"`
			Config    string                `json:"original_image_config"`
			Source    string                `json:"source"`
		}
		if e := json.Unmarshal(body, &receipt); e != nil {
			t.Fatal("invalid retained receipt")
		}
		if receipt.Package.Organization != trial.org || receipt.Package.Name != packageName || receipt.Package.ID != ackID || receipt.Package.RepositoryID != repoID || receipt.Digest != expected.String() || receipt.Reference != digestReference.String() || receipt.Config != configHash.String() || receipt.Source != source {
			t.Fatal("retained receipt does not match exact source/package/digest acknowledgement")
		}
		identity, e = trial.ReadContainerPackageIdentity(t.Context(), packageName, repoID)
		if e != nil || identity != receipt.Package {
			t.Fatal("retained package identity changed; refusing adoption")
		}
		retained = true
	} else {
		t.Logf("source image publication starting: package=%s source=%s original_config=%s", packageName, source, configHash)
		writeErr = remote.Write(reference, mirrored, options...)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		identity, err = retry.Do(ctx, nil, 15, 2*time.Second, func() (GitHubPackageIdentity, error) {
			observed, readErr := trial.ReadContainerPackageIdentity(ctx, packageName, repoID)
			if readErr == nil || errors.Is(readErr, os.ErrNotExist) {
				return observed, readErr
			}
			return observed, retry.Permanent(readErr)
		}, retry.WithBackoff(retry.Constant))
		if err != nil {
			t.Fatalf("published package identity unavailable; refusing adoption/deletion; inspect exact coordinate before retry: %v", err)
		}
	}
	t.Cleanup(func() {
		if retained {
			t.Logf("explicit retained package pending public confirmation/cleanup: package=%s id=%d repository=%d", identity.Name, identity.ID, identity.RepositoryID)
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
		defer cancel()
		if e := trial.DeleteContainerPackage(ctx, identity); e != nil {
			t.Error(e)
		} else {
			t.Logf("source image cleanup confirmed: package=%s id=%d repository=%d", identity.Name, identity.ID, identity.RepositoryID)
		}
	})
	if writeErr != nil {
		t.Fatal("source image upload failed; exact owned package scheduled for cleanup")
	}
	pkg, _, metadataErr := trial.api.Organizations.GetPackage(t.Context(), trial.org, "container", packageName)
	visibility := "unavailable"
	if metadataErr == nil {
		visibility = pkg.GetVisibility()
	}
	authenticated, authenticatedErr := remote.Head(digestReference, options...)
	servedDigest := "unavailable"
	if authenticatedErr == nil {
		servedDigest = authenticated.Digest.String()
	}
	uploadObservation, _ := json.MarshalIndent(map[string]any{"package": identity, "visibility": visibility, "expected_manifest_digest": expected.String(), "authenticated_served_digest": servedDigest, "reference": digestReference.String(), "original_image_config": configHash.String(), "source": source, "version": config.Config.Labels["org.opencontainers.image.version"]}, "", "  ")
	if err := writeSnapshotEvidence(snapshotEvidenceRoot(), "source-image-upload-observation.json", uploadObservation); err != nil {
		t.Fatal(err)
	}
	t.Logf("source image uploaded: id=%d repository=%d visibility=%s served_digest=%s", identity.ID, identity.RepositoryID, visibility, servedDigest)
	if authenticatedErr != nil || servedDigest != expected.String() {
		t.Fatal("authenticated served manifest digest not proven")
	}
	// Verify authenticated payload/layer identity before retaining a private
	// package for the supported settings-UI transition.
	served, err := remote.Image(digestReference, options...)
	if err != nil {
		var registryError *transport.Error
		if errors.As(err, &registryError) {
			t.Logf("authenticated image read HTTP=%d", registryError.StatusCode)
		}
		t.Fatal("published image payload unavailable")
	}
	servedConfig, err := served.ConfigFile()
	if err != nil || servedConfig.Architecture != "amd64" || servedConfig.Config.Labels["org.opencontainers.image.revision"] != source {
		t.Fatal("served source/platform mismatch")
	}
	originalLayers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	servedLayers, err := served.Layers()
	if err != nil || len(originalLayers) != len(servedLayers) {
		t.Fatal("mirror changed image layers")
	}
	for index, layer := range originalLayers {
		a, e := layer.Digest()
		if e != nil {
			t.Fatal(e)
		}
		b, e := servedLayers[index].Digest()
		if e != nil || a != b {
			t.Fatal("mirror layer bytes changed")
		}
	}
	if phase == "publish-retain" || (phase == "resume" && visibility != "public") {
		if metadataErr != nil {
			t.Fatal("cannot retain without package metadata")
		}
		encoded, _ := json.Marshal(pkg)
		var packageFields map[string]any
		_ = json.Unmarshal(encoded, &packageFields)
		htmlURL, _ := packageFields["html_url"].(string)
		settingsURL, e := sourceImageSettingsURL(trial.org, packageName, htmlURL)
		if e != nil {
			// Some GitHub API HTML URLs are version-specific, not settings
			// roots. Never follow an unverified URL: derive the canonical UI
			// route solely from the already-validated exact package identity.
			settingsURL, e = sourceImageSettingsURL(trial.org, packageName, "")
			if e != nil {
				t.Fatal(e)
			}
		}
		receipt, _ := json.MarshalIndent(map[string]any{"package": identity, "visibility": visibility, "expected_manifest_digest": expected.String(), "authenticated_served_digest": servedDigest, "reference": digestReference.String(), "original_image_config": configHash.String(), "source": source, "version": servedConfig.Config.Labels["org.opencontainers.image.version"], "platform": "linux/amd64", "layer_bytes_preserved": true, "settings_url": settingsURL, "api_html_url": htmlURL, "status": "pending-public-confirmation; exact resource retained; cleanup pending"}, "", "  ")
		if e := writeSnapshotEvidence(snapshotEvidenceRoot(), "source-image-retained.json", receipt); e != nil {
			t.Fatal(e)
		}
		retained = true
		t.Skipf("PENDING_PUBLIC_CONFIRMATION package_id=%d settings=%s; retained by explicit acknowledgement, no PR launched", identity.ID, settingsURL)
	}
	publicImage, e := remote.Image(digestReference, remote.WithContext(t.Context()), remote.WithAuth(authn.Anonymous))
	if e != nil {
		t.Fatal("published image is not anonymously readable")
	}
	publicConfig, e := publicImage.ConfigFile()
	if e != nil || publicConfig.Architecture != "amd64" || publicConfig.Config.Labels["org.opencontainers.image.revision"] != source {
		t.Fatal("anonymous image config/source proof failed")
	}
	pkg, _, err = trial.api.Organizations.GetPackage(t.Context(), trial.org, "container", packageName)
	if err != nil || pkg.GetVisibility() != "public" {
		t.Fatal("package metadata does not assert public visibility")
	}
	retained = false
	proof, _ := json.MarshalIndent(map[string]any{"package": identity, "served_manifest_digest": expected.String(), "reference": digestReference.String(), "original_image_config": configHash.String(), "source": source, "version": servedConfig.Config.Labels["org.opencontainers.image.version"], "platform": "linux/amd64", "public_anonymous_pull": true, "layer_bytes_preserved": true}, "", "  ")
	if err := writeSnapshotEvidence(snapshotEvidenceRoot(), "source-image-publication.json", proof); err != nil {
		t.Fatal(err)
	}
	t.Logf("public source image verified: id=%d repo=%d ref=%s", identity.ID, repoID, digestReference)
	output, consumerErr := trial.output(reporoot.Path(t), "just", "--justfile", reporoot.Path(t)+"/justfile", "test-live-github-consumer", strconv.FormatInt(id, 10), os.Getenv("FORGE_CI_LIVE_CONSUMER"), "pull-request", "fixtures=tests/live/github", "RELEASE_PLATFORMS=linux/amd64", "nanolinter-image="+digestReference.String())
	if err := writeSnapshotEvidence(snapshotEvidenceRoot(), "source-image-consumer-pr.log", []byte(strings.ReplaceAll(output, trial.token, "[REDACTED]"))); err != nil {
		t.Fatal(err)
	}
	if consumerErr != nil {
		t.Fatalf("mature consumer PR failed: %v; inspect retained child transcript", consumerErr)
	}
}
