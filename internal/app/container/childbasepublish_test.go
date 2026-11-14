// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
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
	appbaseimages "github.com/diggsweden/forge-ci/internal/app/baseimages"
	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

func childBaseFixture(t *testing.T) (*rootBaseFixture, appbaseimages.BaseImageParent) {
	t.Helper()

	f := newRootBaseFixture(t)
	// The immutable content revision is not the current checkout/source commit.
	f.in.Plan.Images[0].Revision = f.in.SourceSHA
	parent := appbaseimages.BaseImageParent{Ref: "other.example/shared/parent@sha256:" + strings.Repeat("d", 64), InputID: strings.Repeat("e", 64)}

	f.in.Plan.Images[0].ProvenanceJSON = fmt.Sprintf(`{"material":"opaque-consumer-identity","parent_base":{"ref":%q,"input_id":%q}}`, parent.Ref, parent.InputID)
	for idx := range f.in.Plan.Builds {
		f.in.Plan.Builds[idx].Revision = f.in.SourceSHA
		f.in.Plan.Builds[idx].BuildArgs = "PARENT_IMAGE=" + parent.Ref
		f.in.Plan.Builds[idx].RegistryInputs = map[string]string{"PARENT_IMAGE": parent.Ref}
	}

	f.in.SourceSHA = strings.Repeat("c", 40)

	return f, parent
}

func publishChildBase(t *testing.T, f *rootBaseFixture, parent appbaseimages.BaseImageParent) error {
	t.Helper()

	return appbaseimages.PublishChildBase(t.Context(), f.registry, io.Discard,
		appbaseimages.PublishChildBaseInput{PublishRootBaseInput: f.in, ParentBase: parent})
}

func TestChildBasePublicationBindsCanonicalSignedLineage(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong expected parent ref", "wrong expected parent id", "tampered SBOM", "tampered lineage transport"} {
		t.Run(scenario, func(t *testing.T) {
			fixture, parent := childBaseFixture(t)
			require.NoError(t, appbaseimages.ValidateChildBasePlan(fixture.in.Plan, fixture.in.ExpectedRepository, parent))
			require.Error(t, appbaseimages.ValidateRootBasePlan(fixture.in.Plan, fixture.in.ExpectedRepository))
			require.Error(t, appcontainer.ValidateNativeReleaseImagesPlan(fixture.in.Plan))
			require.NoError(t, publishChildBase(t, fixture, parent))

			if scenario == "tampered lineage transport" {
				digest, err := apprelease.CanonicalPayloadDigest("payload")
				require.NoError(t, err)
				body, err := os.ReadFile("payload/dist/base-images.json")
				require.NoError(t, err)

				body = []byte(strings.ReplaceAll(string(body), parent.InputID, strings.Repeat("f", 64)))
				require.NoError(t, os.WriteFile("payload/dist/base-images.json", body, 0o600))
				require.Error(t, apprelease.StageVerifiedCanonicalPayload(t.Context(), "payload", "verified", digest))
				require.NoDirExists(t, "verified")
				require.Len(t, fixture.registry.writes, 3)

				return
			}

			imported := importPublishedBasePayload(t, fixture)
			require.Equal(t, map[string]any{"ref": parent.Ref, "input_id": parent.InputID}, imported[0].Provenance["parent_base"])
			sign := rootBaseSigningInput(t, fixture, imported)
			evidence, syft := &rootBaseEvidence{}, &fakeLedgerSyft{}

			if scenario == "tampered SBOM" {
				require.NoError(t, os.WriteFile(filepath.Join("verified", imported[0].SBOM), []byte(`{}`), 0o600))
				require.Error(t, appcontainer.SignLedgerImages(t.Context(), evidence, syft, fixture.registry, io.Discard, io.Discard, sign))
				require.Empty(t, evidence.signs)
				require.Empty(t, evidence.attests)

				return
			}

			require.NoError(t, appcontainer.SignLedgerImages(t.Context(), evidence, syft, fixture.registry, io.Discard, io.Discard, sign))
			require.Empty(t, syft.targets)

			producerSBOM, err := os.ReadFile("payload/" + imported[0].SBOM)
			require.NoError(t, err)
			require.Equal(t, producerSBOM, evidence.attests[0].predicate)
			require.Contains(t, string(evidence.attests[1].predicate), parent.InputID)
			require.Contains(t, string(evidence.attests[1].predicate), parent.Ref)

			// Fixture setup only: expose a final tag so the existing verifier can
			// check signed lineage. This does not prove expected-parent promotion.
			require.NoError(t, fixture.registry.CopyTag(t.Context(), imported[0].Ref, imported[0].FinalTag))
			before := len(fixture.registry.writes)

			if scenario == "wrong expected parent ref" {
				parent.Ref = "other.example/wrong/parent@sha256:" + strings.Repeat("d", 64)
			}

			if scenario == "wrong expected parent id" {
				parent.InputID = strings.Repeat("f", 64)
			}

			result, err := appbaseimages.VerifyExistingBaseImages(t.Context(), fixture.registry, evidence, io.Discard, appbaseimages.BaseImageVerifyExistingInput{
				Flavors: []string{"core"}, BaseInputs: []appbaseimages.BaseInput{{Flavor: "core", BaseInputID: imported[0].BaseInputID, ParentBase: &parent}},
				ExpectedRepository: sign.ExpectedImageRepository, ExpectedSource: "https://source.example/org/app", ExpectedWorkflow: "bases.yml", CosignPublicKey: "inert-key",
			})
			if scenario == "valid" {
				require.NoError(t, err)
				require.True(t, result.AllFound)
				require.Contains(t, result.AllImagesJSON, imported[0].Ref)
			} else {
				require.ErrorIs(t, err, errs.ErrValidation)
				require.False(t, result.AllFound)
			}

			require.Len(t, fixture.registry.writes, before, "verification never publishes")
		})
	}
}

// One stage builds every sibling of one parent. Each is held to that parent
// on its own, every layout is gated before the first upload, and a sibling
// that names another parent, in its lineage or as the registry input its
// build argument carries alone, stops the stage before anything is written.
func TestChildBaseStagePublishesSiblingsOfOneParent(t *testing.T) {
	const elsewhere = "other.example/shared/elsewhere@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

	for _, tc := range []struct {
		name, lineage, input, refused string
	}{
		{name: "siblings"},
		{name: "sibling on another parent", lineage: elsewhere, input: elsewhere, refused: "lineage must match"},
		{name: "sibling builds on another parent", input: elsewhere, refused: "registry input differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, parent := childBaseFixture(t)
			id := strings.Repeat("f", 64)
			sibling := fixture.in.Plan.Images[0]
			sibling.Name, sibling.Flavor, sibling.BaseInputID = "tools", "tools", id
			sibling.FinalTagName, sibling.CandidateTag = id+"-tools", "staging-"+id+"-tools"
			sibling.ProvenanceJSON = fmt.Sprintf(`{"parent_base":{"ref":%q,"input_id":%q}}`, cmp.Or(tc.lineage, parent.Ref), parent.InputID)
			fixture.in.Plan.Images = append(fixture.in.Plan.Images, sibling)
			fixture.stage(t, sibling)

			for idx := range fixture.in.Plan.Builds {
				if build := &fixture.in.Plan.Builds[idx]; build.Name == "tools" {
					build.Revision = sibling.Revision
					build.BuildArgs = "PARENT_IMAGE=" + cmp.Or(tc.input, parent.Ref)
					build.RegistryInputs = map[string]string{"PARENT_IMAGE": cmp.Or(tc.input, parent.Ref)}
				}
			}

			err := publishChildBase(t, fixture, parent)
			if tc.refused != "" {
				require.ErrorIs(t, err, errs.ErrInvalidConfig)
				require.ErrorContains(t, err, tc.refused)
				require.Empty(t, fixture.registry.writes)
				require.NoFileExists(t, "payload/dist/base-images.json")

				return
			}

			require.NoError(t, err)
			require.Len(t, fixture.gated, 4)

			ledger, err := os.ReadFile("payload/dist/base-images.json")
			require.NoError(t, err)
			inputs, err := appbaseimages.BaseInputsFromLedger(ledger, fixture.in.ExpectedRepository)
			require.NoError(t, err)
			require.Equal(t, []appbaseimages.BaseInput{
				{Flavor: "core", BaseInputID: fixture.in.Plan.Images[0].BaseInputID, ParentBase: &parent},
				{Flavor: "tools", BaseInputID: id, ParentBase: &parent},
			}, inputs)
		})
	}
}

func TestChildBasePublicationRefusesInvalidInputsBeforeUpload(t *testing.T) {
	for _, defect := range []string{"missing parent", "mutable parent", "malformed parent id", "self id", "missing lineage", "wrong lineage", "extra lineage field", "missing registry input", "wrong registry input", "extra registry input", "conflicting argument", "duplicate argument", "different platform argument", "missing build", "unsafe name", "mixed images", "cache", "missing layout", "last scan fails", "valid postgate replacement"} {
		t.Run(defect, func(t *testing.T) {
			fixture, parent := childBaseFixture(t)

			switch defect {
			case "missing parent":
				parent = appbaseimages.BaseImageParent{}
			case "mutable parent":
				parent.Ref = "other.example/shared/parent:latest"
			case "malformed parent id":
				parent.InputID = "bad"
			case "self id":
				parent.InputID = fixture.in.Plan.Images[0].BaseInputID
			case "missing lineage":
				fixture.in.Plan.Images[0].ProvenanceJSON = `{}`
			case "wrong lineage":
				fixture.in.Plan.Images[0].ProvenanceJSON = strings.ReplaceAll(fixture.in.Plan.Images[0].ProvenanceJSON, parent.InputID, strings.Repeat("f", 64))
			case "extra lineage field":
				fixture.in.Plan.Images[0].ProvenanceJSON = fmt.Sprintf(`{"parent_base":{"ref":%q,"input_id":%q,"extra":true}}`, parent.Ref, parent.InputID)
			case "missing registry input":
				fixture.in.Plan.Builds[1].RegistryInputs = nil
			case "wrong registry input":
				fixture.in.Plan.Builds[1].RegistryInputs["PARENT_IMAGE"] = "other.example/wrong/parent@sha256:" + strings.Repeat("d", 64)
			case "extra registry input":
				fixture.in.Plan.Builds[1].RegistryInputs["OTHER"] = parent.Ref
			case "conflicting argument":
				fixture.in.Plan.Builds[1].BuildArgs += "\nPARENT_IMAGE=other.example/shared/parent:latest"
			case "duplicate argument":
				fixture.in.Plan.Builds[1].BuildArgs += "\nPARENT_IMAGE=" + parent.Ref
			case "different platform argument":
				fixture.in.Plan.Builds[1].BuildArgs = "OTHER=" + parent.Ref
				fixture.in.Plan.Builds[1].RegistryInputs = map[string]string{"OTHER": parent.Ref}
			case "missing build":
				fixture.in.Plan.Builds = fixture.in.Plan.Builds[:1]
			case "unsafe name":
				fixture.in.Plan.Images[0].Name = "core tools"
				for idx := range fixture.in.Plan.Builds {
					fixture.in.Plan.Builds[idx].Name = "core tools"
				}
			case "mixed images":
				fixture.in.Plan.Images = append(fixture.in.Plan.Images, pipeline.ReleaseImage{Name: "other", ImageKind: "release"})
			case "cache":
				fixture.in.Plan.Images[0].CacheTagPrefix = "cache-root"
			case "missing layout":
				require.NoError(t, os.Rename("layouts/root-arch-arm64", "unselected"))
			case "last scan fails":
				fixture.in.Gate = func(_ context.Context, _, platform string) error {
					if platform == "linux/arm64" {
						return errs.ErrValidation
					}

					return nil
				}
			case "valid postgate replacement":
				fixture.in.Gate = func(ctx context.Context, _, platform string) error {
					if platform == "linux/arm64" {
						replaceChildLayout(ctx, t, fixture)
					}

					return nil
				}
			}

			err := publishChildBase(t, fixture, parent)
			require.Error(t, err)

			switch defect {
			case "valid postgate replacement":
				require.ErrorContains(t, err, "digest changed after admission")
			case "unsafe name":
				require.ErrorContains(t, err, "base image name")
			}

			require.Empty(t, fixture.registry.writes)
			require.NoFileExists(t, "payload/dist/base-images.json")
		})
	}
}

// Replace both local bytes and records with a different, internally valid image.
// Admission pins, not the rewritten producer records, must stop publication.
func replaceChildLayout(ctx context.Context, t *testing.T, fixture *rootBaseFixture) {
	t.Helper()

	directory := "layouts/root-arch-amd64/layout"
	in, image := stagedLayout(t, ctx, directory, fixture.in.ExpectedRepository+":"+fixture.in.Plan.Images[0].CandidateTag+"-amd64", "amd64")
	config, err := image.ConfigFile()
	require.NoError(t, err)

	config.Config.Labels["replacement"] = "valid-but-not-scanned"
	image, err = mutate.ConfigFile(image, config)
	require.NoError(t, err)
	path, err := layout.Write(directory, empty.Index)
	require.NoError(t, err)
	require.NoError(t, path.AppendImage(image))

	record, err := appcontainer.RecordImageLayout(ctx, ociregistry.NewAnonymous(), in)
	require.NoError(t, err)
	body, err := json.Marshal(appcontainer.SignerImageArchMetadata{Arch: "amd64", Tag: in.ImageRef, Digest: record.Digest, Ref: fixture.in.ExpectedRepository + "@" + record.Digest})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("layouts/root-arch-amd64/root-amd64.json", body, 0o600))
}
