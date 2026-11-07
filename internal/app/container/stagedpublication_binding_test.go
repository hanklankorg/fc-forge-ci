// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// Adapted from the independent review's coherent replacement reproducer. Both
// content records and architecture metadata are regenerated, so ordinary layout
// validation still passes. Only the independently retained admission pin differs.
func replaceStagedPublicationLayout(t *testing.T, ctx context.Context, layouts string, image pipeline.ReleaseImage, arch string) (string, string) {
	t.Helper()

	directory := filepath.Join(layouts, image.Name+"-arch-"+arch, "layout")
	original, err := layout.FromPath(directory)
	require.NoError(t, err)
	index, err := original.ImageIndex()
	require.NoError(t, err)
	manifest, err := index.IndexManifest()
	require.NoError(t, err)

	before := manifest.Manifests[0].Digest
	content, err := original.Image(before)
	require.NoError(t, err)
	config, err := content.ConfigFile()
	require.NoError(t, err)

	config.Config.Labels["review.changed.after.scan"] = "true"
	changed, err := mutate.ConfigFile(content, config)
	require.NoError(t, err)
	workspace := t.TempDir()
	replacementDir := filepath.Join(workspace, "replacement")
	replacement, err := layout.Write(replacementDir, empty.Index)
	require.NoError(t, err)
	require.NoError(t, replacement.AppendImage(changed))

	tag := image.ImageRepository + ":" + image.CandidateTag + "-" + arch
	_, err = appcontainer.RecordImageLayout(ctx, ociregistry.NewAnonymous(), appcontainer.LayoutRecordInput{
		Directory: replacementDir, ImageRef: tag, Platform: "linux/" + arch,
		SourceSHA: config.Config.Labels["org.opencontainers.image.revision"],
	})
	require.NoError(t, err)
	require.NoError(t, os.Rename(directory, filepath.Join(workspace, "old-layout")))
	require.NoError(t, os.Rename(replacementDir, directory))

	after, err := changed.Digest()
	require.NoError(t, err)
	body, err := json.Marshal(appcontainer.SignerImageArchMetadata{Arch: arch, Tag: tag, Digest: after.String(), Ref: image.ImageRepository + "@" + after.String()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(directory), image.Name+"-"+arch+".json"), body, 0o600))
	checked, err := ociregistry.NewAnonymous().InspectLayout(ctx, directory)
	require.NoError(t, err)
	require.Equal(t, "linux/"+arch, checked.Platform)
	require.Equal(t, config.Config.Labels["org.opencontainers.image.revision"], checked.Revision)
	require.NotEqual(t, before.String(), checked.Digest)

	return before.String(), after.String()
}

func TestReviewRootBaseCoherentReplacementAfterGate(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			fixture := newRootBaseFixture(t)

			var before, after string

			fixture.in.Gate = func(ctx context.Context, _, platform string) error {
				fixture.gated = append(fixture.gated, platform)
				if platform == "linux/arm64" {
					before, after = replaceStagedPublicationLayout(t, ctx, fixture.in.LayoutsDir, fixture.in.Plan.Images[0], arch)
				}

				return nil
			}

			err := fixture.publish(t.Context())
			require.ErrorIs(t, err, errs.ErrValidation)
			require.ErrorContains(t, err, "staged linux/"+arch+" layout digest changed after admission")
			require.ErrorContains(t, err, before)
			require.ErrorContains(t, err, after)
			require.Empty(t, fixture.registry.writes, "no platform may be uploaded after any admission pin changes")
			require.Equal(t, []string{"linux/amd64", "linux/arm64"}, fixture.gated)
			require.Len(t, fixture.syft.targets, 2, "do not rescan or regenerate inventories for the replacement")

			canonical, err := os.ReadFile("payload/premade-sboms/base-sbom-core.cyclonedx.json")
			require.NoError(t, err)
			inventory, err := os.ReadFile("payload/premade-sboms/inventory-root-amd64.cyclonedx.json")
			require.NoError(t, err)
			require.Equal(t, inventory, canonical, "the original producer SBOM remains unchanged")
			require.NoFileExists(t, "payload/dist/base-images.json")
		})
	}
}

func TestStagedPublicationPinsEveryPlatformBeforePolicyCallbacks(t *testing.T) {
	for _, target := range []string{"unchanged", "amd64", "arm64"} {
		t.Run(target, func(t *testing.T) {
			fixture := newRootBaseFixture(t)
			image := fixture.in.Plan.Images[0]

			meta, err := appcontainer.PublishAndAssembleSignerImage(t.Context(), fixture.registry, nil, nil, io.Discard, appcontainer.SignerImageAssembleInput{
				LayoutsDir: fixture.in.LayoutsDir, AuthFile: fixture.in.AuthFile, SourceSHA: fixture.in.SourceSHA,
				ServerURL: fixture.in.ServerURL, Repository: fixture.in.Repository, ImageRepository: image.ImageRepository,
				TagName: image.CandidateTag, Name: image.Name, Archs: strings.Fields(image.Archs), MetadataDir: "assembly", RetryAttempts: 1,
				RequiredLabels: []string{image.RequiredLabels},
				Evidence: &appcontainer.StagedLayoutEvidence{CheckLayout: func(ctx context.Context, _, platform string) error {
					fixture.gated = append(fixture.gated, platform)
					if platform == "linux/arm64" && target != "unchanged" {
						_, _ = replaceStagedPublicationLayout(t, ctx, fixture.in.LayoutsDir, image, target)
					}

					return nil
				}},
			})
			if target == "unchanged" {
				require.NoError(t, err)
				served, resolveErr := fixture.registry.ResolveDigest(t.Context(), meta.Tag)
				require.NoError(t, resolveErr)
				require.Equal(t, meta.Digest, served)
				require.Len(t, fixture.registry.writes, 3)
			} else {
				require.ErrorIs(t, err, errs.ErrValidation)
				require.ErrorContains(t, err, "layout digest changed after admission")
				require.Empty(t, fixture.registry.writes)
				require.Nil(t, meta)
			}
		})
	}
}

func TestReleasePublicationRejectsCoherentReplacementAcrossCompletePlan(t *testing.T) {
	for _, target := range []string{"unchanged", "app/amd64", "app/arm64", "worker/amd64", "worker/arm64"} {
		t.Run(target, func(t *testing.T) {
			t.Chdir(t.TempDir())

			repository := "registry.example/org/app"
			plan := pipeline.ReleaseImagesPlan{}

			for _, name := range []string{"app", "worker"} {
				plan.Images = append(plan.Images, pipeline.ReleaseImage{
					Name: name, ImageRepository: repository, CandidateTag: "staging-v1.2.3-" + name,
					FinalTagName: "v1.2.3-" + name, Archs: "amd64\narm64", Scan: true,
				})
			}

			require.NoError(t, os.WriteFile("auth.json", []byte(`{"auths":{}}`), 0o600))

			for _, image := range plan.Images {
				writePublicationLayouts(t, image)
			}

			gated := []string{}
			registry := &rootBaseRegistry{LayoutInspector: ociregistry.NewAnonymous(), raw: map[string][]byte{}, images: map[string]domaincontainer.LayoutImage{}, onWrite: func() {
				require.Equal(t, []string{"app-arch-amd64", "app-arch-arm64", "worker-arch-amd64", "worker-arch-arm64"}, gated)
			}}
			scanner := &fakeImageEvidenceTrivy{}
			err := appcontainer.PublishReleaseImages(t.Context(), registry, nil, io.Discard, appcontainer.PublishReleaseImagesInput{
				Plan: plan, LayoutsDir: "layouts", OutputDir: "published", AuthFile: "auth.json",
				ReleaseTag: "v1.2.3", ReleaseSHA: strings.Repeat("b", 40), ServerURL: "https://source.example", Repository: "org/app", RetryAttempts: 1,
				Trivy: scanner,
				Gate: func(ctx context.Context, directory, _ string) error {
					name := filepath.Base(filepath.Dir(directory))
					gated = append(gated, name)

					if name == "worker-arch-arm64" && target != "unchanged" {
						imageName, arch, _ := strings.Cut(target, "/")
						for _, image := range plan.Images {
							if image.Name == imageName {
								_, _ = replaceStagedPublicationLayout(t, ctx, "layouts", image, arch)
							}
						}
					}

					return nil
				},
			})
			require.Len(t, scanner.calls, 4, "scan each platform once, never accept replacement through a rescan")

			if target == "unchanged" {
				require.NoError(t, err)
				require.Len(t, registry.writes, 6)
				require.FileExists(t, "published/release-images.json")
			} else {
				require.ErrorIs(t, err, errs.ErrValidation)
				require.ErrorContains(t, err, "layout digest changed after admission")
				require.Empty(t, registry.writes, "recheck all images, including later ones, before any upload")
				require.NoFileExists(t, "published/release-images.json")
			}
		})
	}
}

func writePublicationLayouts(t *testing.T, image pipeline.ReleaseImage) {
	t.Helper()

	for arch := range strings.FieldsSeq(image.Archs) {
		directory := filepath.Join("layouts", image.Name+"-arch-"+arch)
		tag := image.ImageRepository + ":" + image.CandidateTag + "-" + arch
		_, content := stagedLayout(t, t.Context(), filepath.Join(directory, "layout"), tag, arch)
		digest, err := content.Digest()
		require.NoError(t, err)
		body, err := json.Marshal(appcontainer.SignerImageArchMetadata{Arch: arch, Tag: tag, Digest: digest.String(), Ref: image.ImageRepository + "@" + digest.String()})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, image.Name+"-"+arch+".json"), body, 0o600))
	}
}
