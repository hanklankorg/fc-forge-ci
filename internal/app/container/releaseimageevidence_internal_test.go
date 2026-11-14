// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

func TestMapReleaseImageEvidenceCanonicalPaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		image pipeline.ReleaseImage
		want  releaseImageEvidenceMapping
	}{
		{"default release name", pipeline.ReleaseImage{Name: "app"}, releaseImageEvidenceMapping{
			outputSBOM: "image-sbom-app.cyclonedx.json", signerSBOM: "dist/image-sbom-app.cyclonedx.json", ledger: "dist/release-images.json",
		}},
		{"release role", pipeline.ReleaseImage{Name: "app", Role: "worker", ImageKind: "release"}, releaseImageEvidenceMapping{
			outputSBOM: "image-sbom-worker.cyclonedx.json", signerSBOM: "dist/image-sbom-worker.cyclonedx.json", ledger: "dist/release-images.json",
		}},
		{"release flavor wins", pipeline.ReleaseImage{Name: "app", Role: "worker", Flavor: "core.v1-2_3"}, releaseImageEvidenceMapping{
			outputSBOM: "image-sbom-core.v1-2_3.cyclonedx.json", signerSBOM: "dist/image-sbom-core.v1-2_3.cyclonedx.json", ledger: "dist/release-images.json",
		}},
		{"base flavor", pipeline.ReleaseImage{Name: "app", Role: "base", Flavor: "core.v1-2_3", ImageKind: "base"}, releaseImageEvidenceMapping{
			outputSBOM: "premade-sboms/base-sbom-core.v1-2_3.cyclonedx.json", signerSBOM: "premade-sboms/base-sbom-core.v1-2_3.cyclonedx.json", ledger: "dist/base-images.json",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mapReleaseImageEvidence(tc.image)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			file, err := releaseImageSBOMFile(PublishReleaseImagesInput{OutputDir: "publisher"}, tc.image)
			require.NoError(t, err)
			require.Equal(t, filepath.Join("publisher", tc.want.outputSBOM), file)
		})
	}
}

func TestMapReleaseImageEvidenceRejectsUnconfinedPaths(t *testing.T) {
	for _, kind := range []string{"release", "base"} {
		for _, flavor := range []string{"../escape", "a/../../escape", "/absolute", `a\escape`, "a\nb", "a\rb", "a\x00b"} {
			t.Run(kind+"/"+flavor, func(t *testing.T) {
				got, err := mapReleaseImageEvidence(pipeline.ReleaseImage{ImageKind: kind, Flavor: flavor})
				require.ErrorIs(t, err, errs.ErrInvalidConfig)
				require.Zero(t, got)
			})
		}
	}

	for _, image := range []pipeline.ReleaseImage{{}, {ImageKind: "base", Role: "base"}, {ImageKind: "other", Flavor: "core"}, {Role: "../escape"}, {Name: "../escape"}} {
		got, err := mapReleaseImageEvidence(image)
		require.ErrorIs(t, err, errs.ErrInvalidConfig)
		require.Zero(t, got)
	}
}

func TestMapReleaseImageEvidenceCollisionsAndSeparateLedgers(t *testing.T) {
	first, err := mapReleaseImageEvidence(pipeline.ReleaseImage{Name: "app", Flavor: "core"})
	require.NoError(t, err)
	second, err := mapReleaseImageEvidence(pipeline.ReleaseImage{Name: "worker", Role: "worker", Flavor: "core"})
	require.NoError(t, err)
	require.Equal(t, first, second, "different names must not hide a shared flavor output collision")

	base, err := mapReleaseImageEvidence(pipeline.ReleaseImage{ImageKind: "base", Flavor: "core"})
	require.NoError(t, err)
	require.NotEqual(t, first.outputSBOM, base.outputSBOM)
	require.NotEqual(t, first.signerSBOM, base.signerSBOM)
	require.NotEqual(t, first.ledger, base.ledger)
	// Mapping is naming only: the native lifecycle remains explicitly refused.
	require.ErrorIs(t, ValidateNativeReleaseImagesPlan(pipeline.ReleaseImagesPlan{Images: []pipeline.ReleaseImage{{ImageKind: "base", Flavor: "core"}}}), errs.ErrInvalidConfig)
}
