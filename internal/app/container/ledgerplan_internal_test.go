// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// Admission must judge the path the publication step will actually record,
// including flavor precedence and the role/name fallback. Testing the actual
// flag shaper against the plan catches drift between these two boundaries.
func TestResolvedImagePlan_UsesThePublicationSBOMPolicy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, flavor, role, sbom string
		refused                  bool
	}{
		{name: "flavor takes precedence", flavor: "rust", role: "flavor", sbom: "dist/image-sbom-rust.cyclonedx.json"},
		{name: "unused role is not a path", flavor: "rust", role: "../../../unused", sbom: "dist/image-sbom-rust.cyclonedx.json"},
		{name: "role override", role: "distroless", sbom: "dist/image-sbom-distroless.cyclonedx.json"},
		{name: "name fallback", sbom: "dist/image-sbom-app.cyclonedx.json"},
		{name: "unsafe flavor", flavor: "../../../escape", role: "flavor", refused: true},
		{name: "unsafe role fallback", role: "../../../escape", refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const (
				repository = "registry.invalid/acme/app"
				tag        = "v1.2.3"
			)

			plan, err := pipeline.PlanReleaseImages([]pipeline.PlannedContainer{{
				Name: "app", ContainerFile: "Containerfile", Context: ".", Platforms: "linux/amd64",
			}}, pipeline.ReleaseImagesInput{ImageRepository: repository, Repository: "acme/app", ReleaseTag: tag})
			require.NoError(t, err)

			plan.Images[0].Role, plan.Images[0].Flavor = tc.role, tc.flavor
			body, err := json.Marshal(plan)
			require.NoError(t, err)

			_, admissionErr := pipeline.ReadImageBuildPlan(string(body), repository, tag)

			role := tc.role
			if role == "" {
				role = plan.Images[0].Name
			}

			entry, releaseTag, err := ledgerAddEntryFromFlags(AddLedgerEntryInput{
				ReleaseTag: tag, ImageName: repository, FinalTagName: plan.Images[0].FinalTagName,
				Role: role, Flavor: tc.flavor, ImageKind: string(imageledger.ImageKindRelease),
				Digest: "sha256:" + strings.Repeat("a", 64), DeriveCandidateTag: true, DefaultSBOM: true,
			})
			require.NoError(t, err)

			publicationErr := entry.Validate(releaseTag)
			if tc.refused {
				require.ErrorContains(t, publicationErr, "sbom")
				require.ErrorContains(t, admissionErr, "sbom", "invalid publication paths must fail before builds are scheduled")

				return
			}

			require.NoError(t, admissionErr)
			require.NoError(t, publicationErr)
			require.Equal(t, tc.sbom, entry.SBOM)
		})
	}
}

func TestLedgerAddSBOM_ExplicitPathWinsOverUnusedDefaultInputs(t *testing.T) {
	t.Parallel()

	path, err := ledgerAddSBOM(AddLedgerEntryInput{
		SBOM: "dist/approved.cyclonedx.json", DefaultSBOM: true,
		Flavor: "../../../unused", Role: "../../../unused",
	})
	require.NoError(t, err)
	require.Equal(t, "dist/approved.cyclonedx.json", path)
}

func TestLedgerAddSBOM_BaseDefaultMatchesThePremadeSignerContract(t *testing.T) {
	t.Parallel()

	path, err := ledgerAddSBOM(AddLedgerEntryInput{
		ImageKind: string(imageledger.ImageKindBase), Flavor: "rust", Role: "base", DefaultSBOM: true,
	})
	require.NoError(t, err)
	require.Equal(t, "premade-sboms/base-sbom-rust.cyclonedx.json", path)
}
