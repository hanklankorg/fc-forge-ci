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

	flavor, err := sourceImageFlavor(os.Getenv("FORGE_CI_LIVE_IMAGE_FLAVOR"))
	if err != nil {
		t.Fatal(err)
	}

	batchRetain, err := sourceImageBatchRetention(phase, os.Getenv("FORGE_CI_LIVE_IMAGE_BATCH_RETAIN_ACK"))
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("source image phase admitted: phase=%s flavor=%s", phase, flavor)

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

	image, err := tarball.ImageFromPath(archive, nil)
	if err != nil {
		t.Fatal("image archive unavailable")
	}

	config, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}

	configHash, err := image.ConfigName()
	if err != nil || !sourceImageMatches(config, configHash, source, configID, flavor) {
		t.Fatal("archive image/source/native flavor identity mismatch")
	}

	packageName := repo + "/nanolinter-ci-" + flavor + "-" + source[:12]
	organization := os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_LOGIN")
	// Only mirror metadata changes: preserve the actual upstream source in a
	// separate label, retaining revision/version and every executable layer.
	config.Config.Labels["org.nanolinter.upstream-source"] = config.Config.Labels["org.opencontainers.image.source"]
	config.Config.Labels["org.opencontainers.image.source"] = "https://github.com/" + organization + "/" + repo

	mirrored, err := mutate.ConfigFile(image, config)
	if err != nil {
		t.Fatal(err)
	}

	reference, err := name.NewTag("ghcr.io/" + strings.ToLower(organization) + "/" + packageName + ":source")
	if err != nil {
		t.Fatal(err)
	}

	expected, err := mirrored.Digest()
	if err != nil {
		t.Fatal(err)
	}

	digestReference := reference.Context().Digest(expected.String())
	retained := false

	var (
		writeErr error
		identity GitHubPackageIdentity
		versions []sourceImageVersion
	)

	uploadAcknowledged := false

	if phase == "resume" || phase == "cleanup" {
		path := os.Getenv("FORGE_CI_LIVE_IMAGE_RESUME_RECEIPT")

		ackID, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_IMAGE_ACK_PACKAGE_ID"), 10, 64)
		if err != nil || ackID <= 0 || !filepath.IsAbs(path) {
			t.Fatal("resume requires explicit receipt and package ID")
		}

		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}

		body, err := io.ReadAll(io.LimitReader(file, 65537))
		_ = file.Close()

		if err != nil || len(body) > 65536 {
			t.Fatal("resume receipt unavailable or oversized")
		}

		var receipt sourceImageReceipt
		if err := json.Unmarshal(body, &receipt); err != nil {
			t.Fatal("invalid retained receipt")
		}

		acknowledged := sourceImageReceipt{Package: GitHubPackageIdentity{Organization: organization, Name: packageName, ID: ackID, RepositoryID: repoID}, Digest: expected.String(), Reference: digestReference.String(), Config: configHash.String(), Source: source}
		if !sourceImageReceiptMatches(receipt, acknowledged) {
			t.Fatal("retained receipt does not match exact source/package/digest acknowledgement")
		}

		identity, versions = receipt.Package, receipt.Versions
		retained = true
	}

	// Local phase/archive/receipt admission must finish before provider reads.
	trial := OpenGitHubTrial(t, id)
	if trial.org != organization || !strings.HasPrefix(repo, trial.prefix) {
		t.Fatal("runtime organization/repository mismatch")
	}

	repository, _, err := trial.api.Repositories.Get(t.Context(), trial.org, repo)
	if err != nil || repository.GetID() != repoID || repository.GetPrivate() {
		t.Fatal("public retained engine identity mismatch")
	}

	actor, _, err := trial.api.Users.Get(t.Context(), "")
	if err != nil {
		t.Fatal("actor metadata unavailable")
	}

	options := []remote.Option{remote.WithContext(t.Context()), remote.WithAuth(authn.FromConfig(authn.AuthConfig{Username: actor.GetLogin(), Password: trial.token}))}
	if phase == "resume" || phase == "cleanup" {
		if err := trial.checkSourceImageVersions(t.Context(), identity, versions); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := trial.ReadContainerPackageIdentity(t.Context(), packageName, repoID); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("publication destination already exists or is not safely readable")
		}

		t.Logf("source image publication starting: package=%s source=%s original_config=%s", packageName, source, configHash)

		writeErr = remote.Write(reference, mirrored, options...)
		uploadAcknowledged = writeErr == nil

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
			t.Logf("explicit retained package pending caller confirmation/cleanup: package=%s id=%d repository=%d", identity.Name, identity.ID, identity.RepositoryID)

			return
		}

		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
		defer cancel()

		if err := trial.deleteSourceImagePackage(ctx, identity, versions); err != nil {
			t.Error(err)
		} else {
			t.Logf("source image cleanup confirmed: package=%s id=%d repository=%d", identity.Name, identity.ID, identity.RepositoryID)
		}
	})

	if writeErr != nil {
		t.Fatal("source image upload failed; exact owned package scheduled for cleanup")
	}

	if phase == "cleanup" {
		// Receipt and immutable package identity were checked above. Do not
		// republish or launch another consumer just to release a batch fixture.
		retained = false

		return
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

	uploadObservation := mustSourceImageJSON(t, map[string]any{"phase": phase, "upload_acknowledged": uploadAcknowledged, "package": identity, "visibility": visibility, "expected_manifest_digest": expected.String(), "authenticated_served_digest": servedDigest, "reference": digestReference.String(), "original_image_config": configHash.String(), "source": source, "version": config.Config.Labels["org.opencontainers.image.version"]})
	if err := writeSnapshotEvidence(snapshotEvidenceRoot(), "source-image-upload-observation.json", uploadObservation); err != nil {
		t.Fatal(err)
	}

	t.Logf("source image authenticated observation: phase=%s upload_acknowledged=%t id=%d repository=%d visibility=%s served_digest=%s", phase, uploadAcknowledged, identity.ID, identity.RepositoryID, visibility, servedDigest)

	if authenticatedErr != nil || servedDigest != expected.String() {
		t.Fatal("authenticated served manifest digest not proven")
	}
	// Verify authenticated payload/layer identity before retaining a private
	// package for the supported settings-UI transition.
	served, err := remote.Image(digestReference, options...)
	if err != nil {
		if registryError, ok := errors.AsType[*transport.Error](err); ok {
			t.Logf("authenticated image read HTTP=%d", registryError.StatusCode)
		}

		t.Fatal("published image payload unavailable")
	}

	servedConfig, err := served.ConfigFile()
	if err != nil || servedConfig.OS != "linux" || servedConfig.Architecture != "amd64" || servedConfig.Config.Labels["org.opencontainers.image.revision"] != source || servedConfig.Config.Labels["org.nanolinter.flavor"] != flavor {
		t.Fatal("served source/platform mismatch")
	}

	if !sourceImageLayersMatch(image, served) {
		t.Fatal("mirror layer bytes changed")
	}

	if phase != "resume" {
		versions, err = trial.readSourceImageVersions(t.Context(), identity, expected.String())
		if err != nil {
			t.Fatal(err)
		}
	}

	if phase == "publish-retain" || (phase == "resume" && visibility != "public") {
		if metadataErr != nil {
			t.Fatal("cannot retain without package metadata")
		}

		encoded, err := json.Marshal(pkg)
		if err != nil {
			t.Fatal(err)
		}

		var packageFields map[string]any

		_ = json.Unmarshal(encoded, &packageFields)
		htmlURL, _ := packageFields["html_url"].(string)

		settingsURL, err := sourceImageSettingsURL(trial.org, packageName, htmlURL)
		if err != nil {
			// Some GitHub API HTML URLs are version-specific, not settings
			// roots. Never follow an unverified URL: derive the canonical UI
			// route solely from the already-validated exact package identity.
			settingsURL, err = sourceImageSettingsURL(trial.org, packageName, "")
			if err != nil {
				t.Fatal(err)
			}
		}

		receipt := mustSourceImageJSON(t, map[string]any{"package": identity, "versions": versions, "visibility": visibility, "expected_manifest_digest": expected.String(), "authenticated_served_digest": servedDigest, "reference": digestReference.String(), "original_image_config": configHash.String(), "source": source, "version": servedConfig.Config.Labels["org.opencontainers.image.version"], "platform": "linux/amd64", "layer_bytes_preserved": true, "settings_url": settingsURL, "api_html_url": htmlURL, "status": "pending-public-confirmation; exact resource retained; cleanup pending"})
		if err := writeSnapshotEvidence(snapshotEvidenceRoot(), "source-image-retained.json", receipt); err != nil {
			t.Fatal(err)
		}

		retained = true

		t.Skipf("PENDING_PUBLIC_CONFIRMATION package_id=%d settings=%s; retained by explicit acknowledgement, no PR launched", identity.ID, settingsURL)
	}

	publicImage, err := remote.Image(digestReference, remote.WithContext(t.Context()), remote.WithAuth(authn.Anonymous))
	if err != nil {
		t.Fatal("published image is not anonymously readable")
	}

	publicConfig, err := publicImage.ConfigFile()
	if err != nil || publicConfig.OS != "linux" || publicConfig.Architecture != "amd64" || publicConfig.Config.Labels["org.opencontainers.image.revision"] != source || publicConfig.Config.Labels["org.nanolinter.flavor"] != flavor {
		t.Fatal("anonymous image config/source proof failed")
	}

	pkg, _, err = trial.api.Organizations.GetPackage(t.Context(), trial.org, "container", packageName)
	if err != nil || pkg.GetVisibility() != "public" {
		t.Fatal("package metadata does not assert public visibility")
	}

	retained = batchRetain

	proof := mustSourceImageJSON(t, map[string]any{"package": identity, "versions": versions, "served_manifest_digest": expected.String(), "reference": digestReference.String(), "original_image_config": configHash.String(), "source": source, "version": servedConfig.Config.Labels["org.opencontainers.image.version"], "platform": "linux/amd64", "public_anonymous_pull": true, "layer_bytes_preserved": true, "batch_retained": retained})
	if err := writeSnapshotEvidence(snapshotEvidenceRoot(), "source-image-publication.json", proof); err != nil {
		t.Fatal(err)
	}

	t.Logf("public source image verified: id=%d repo=%d ref=%s", identity.ID, repoID, digestReference)

	if retained {
		// The caller owns both consumer trials and exact-package cleanup after
		// their completion. Qualification itself launches no consumer workflow.
		return
	}

	output, consumerErr := trial.outputEnv(reporoot.Path(t), nil, "just", "--justfile", reporoot.Path(t)+"/justfile", "test-live-consumer", "github", os.Getenv("FORGE_CI_LIVE_CONSUMER"), "pull-request", "org="+strconv.FormatInt(id, 10), "fixtures=tests/live/github", "RELEASE_PLATFORMS=linux/amd64", "nanolinter-image="+digestReference.String())
	if err := writeSnapshotEvidence(snapshotEvidenceRoot(), "source-image-consumer-pr.log", []byte(strings.ReplaceAll(output, trial.token, "[REDACTED]"))); err != nil {
		t.Fatal(err)
	}

	if consumerErr != nil {
		t.Fatalf("mature consumer PR failed: %v; inspect retained child transcript", consumerErr)
	}
}
