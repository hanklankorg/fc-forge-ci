// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package baseimages

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// PublishRootBaseInput describes one resolved root base and its complete staged
// native layout set. OutputDir must be new, with an existing parent outside the
// layout tree. It receives a canonical unsigned payload, not signing authority.
type PublishRootBaseInput struct {
	Plan               pipeline.ReleaseImagesPlan
	ExpectedRepository string
	LayoutsDir         string
	OutputDir          string
	AuthFile           string
	SourceSHA          string
	ServerURL          string
	Repository         string
	RetryAttempts      int
	Trivy              appcontainer.ImageEvidenceTrivy
	Syft               appcontainer.ImageEvidenceSyft
	Gate               func(context.Context, string, string) error
}

// PublishRootBase scans and gates all selected architectures before its first
// registry write, then publishes only unsigned staging candidates. It retains
// every platform inventory and scan under premade-sboms/ and writes the separate
// dist/base-images.json ledger. As in the existing base lifecycle, the pinned
// base SBOM is the first selected platform inventory, not an aggregate index BOM.
//
// The narrow root-base CLI boundary uses this primitive. Parents,
// mixed/multiple images and architecture caches are deliberately unsupported.
// The caller must transport/digest this payload and independently admit it before
// using the existing trusted import, sign and promote use cases.
// A failure can leave evidence or unsigned candidates, but no completed ledger;
// retry with a fresh output directory. Publication is not a registry transaction.
func PublishRootBase(ctx context.Context, registry appcontainer.StagedSignerRegistry, out io.Writer, in PublishRootBaseInput) error {
	image, entry, err := admitRootBasePublication(in)
	if err != nil {
		return err
	}

	return publishBaseStage(ctx, registry, out, in, []pipeline.ReleaseImage{image}, []imageledger.Entry{entry})
}

// Both narrow native boundaries share the complete admission/gate/upload
// barrier: every image of the stage is scanned and gated before the first
// upload, and the ledger is written only once every candidate is pushed.
func publishBaseStage(ctx context.Context, registry appcontainer.StagedSignerRegistry, out io.Writer, in PublishRootBaseInput, images []pipeline.ReleaseImage, entries []imageledger.Entry) error {
	if registry == nil || in.Trivy == nil || in.Syft == nil || in.Gate == nil {
		return fmt.Errorf("native base publication requires registry, scanners and publication gate: %w", errs.ErrMissingInput)
	}

	if len(entries) != len(images) {
		return fmt.Errorf("native base publication has %d ledger entries for %d images: %w", len(entries), len(images), errs.ErrValidation)
	}

	if err := appcontainer.AdmitReleaseImageLayouts(in.LayoutsDir, images); err != nil {
		return err
	}

	if err := createRootBaseOutput(in); err != nil {
		return err
	}

	// Assembly's metadata directory is workspace-relative and is not evidence.
	work, err := os.MkdirTemp(".", ".forge-ci-root-base-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	staged := make([]*appcontainer.StagedSignerImage, len(images))

	for idx, image := range images {
		if staged[idx], err = admitBaseCandidate(ctx, registry, out, in, work, image, &entries[idx]); err != nil {
			return err
		}
	}

	for idx := range entries {
		meta, err := staged[idx].Publish(ctx, registry, nil, nil, out)
		if err != nil {
			return err
		}

		entries[idx].Digest, entries[idx].Ref = meta.Digest, meta.Ref
	}

	ledger, err := imageledger.Marshal(entries)
	if err != nil {
		return err
	}

	return cliio.WriteFile(filepath.Join(in.OutputDir, "dist", "base-images.json"), ledger, 0o644)
}

// admitBaseCandidate admits one image of the stage without uploading it, and
// records in its ledger entry the SBOM the producer retained for it.
func admitBaseCandidate(ctx context.Context, registry appcontainer.StagedSignerRegistry, out io.Writer, in PublishRootBaseInput, work string, image pipeline.ReleaseImage, entry *imageledger.Entry) (*appcontainer.StagedSignerImage, error) {
	metadata := filepath.Join(work, image.Name)
	if err := os.Mkdir(metadata, 0o700); err != nil {
		return nil, err
	}

	assembly := rootBaseAssembleInput(in, image, metadata)
	assembly.Evidence.Stderr = out

	staged, err := appcontainer.AdmitStagedSignerImage(ctx, registry, out, assembly)
	if err != nil {
		return nil, err
	}

	// Pin retained producer bytes before any upload, not a signer-side rescan.
	evidence := filepath.Join(in.OutputDir, "premade-sboms")

	body, err := cliio.ReadRegularFile(filepath.Join(evidence, "inventory-"+image.Name+"-"+strings.Fields(image.Archs)[0]+".cyclonedx.json"))
	if err != nil {
		return nil, err
	}

	entry.SBOMSHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	if err := cliio.WriteFile(filepath.Join(in.OutputDir, entry.SBOM), body, 0o644); err != nil {
		return nil, err
	}

	return staged, nil
}

func rootBaseAssembleInput(in PublishRootBaseInput, image pipeline.ReleaseImage, work string) appcontainer.SignerImageAssembleInput {
	evidence := filepath.Join(in.OutputDir, "premade-sboms")
	revision := cmp.Or(image.Revision, in.SourceSHA)
	required := []string{"org.opencontainers.image.revision=" + revision}

	for line := range strings.SplitSeq(image.RequiredLabels, "\n") {
		if strings.TrimSpace(line) != "" {
			required = append(required, line)
		}
	}

	return appcontainer.SignerImageAssembleInput{
		LayoutsDir: in.LayoutsDir, AuthFile: in.AuthFile, SourceSHA: revision,
		ServerURL: in.ServerURL, Repository: in.Repository, ImageRepository: image.ImageRepository,
		TagName: image.CandidateTag, Name: image.Name, Archs: strings.Fields(image.Archs), MetadataDir: work,
		RequiredLabels: required,
		ManifestOutput: filepath.Join(evidence, "index-"+image.Name+".json"), RetryAttempts: in.RetryAttempts,
		Evidence: &appcontainer.StagedLayoutEvidence{
			Trivy: in.Trivy, Syft: in.Syft, Gate: in.Gate,
			TrivyOutputTemplate: filepath.Join(evidence, "trivy-results-"+image.Name+"-{arch}.json"),
			SBOMOutputTemplate:  filepath.Join(evidence, "inventory-"+image.Name+"-{arch}.cyclonedx.json"),
		},
	}
}

func admitRootBasePublication(in PublishRootBaseInput) (pipeline.ReleaseImage, imageledger.Entry, error) {
	var image pipeline.ReleaseImage

	var entry imageledger.Entry

	images, entries, err := admitBaseStagePublication(in)
	if err != nil {
		return image, entry, err
	}

	// Only siblings share a stage: they have one authenticated parent.
	if len(images) != 1 {
		return image, entry, fmt.Errorf("root base stage requires exactly one base image: %w", errs.ErrInvalidConfig)
	}

	image, entry = images[0], entries[0]

	for _, build := range in.Plan.Builds {
		if len(build.RegistryInputs) != 0 {
			return image, entry, fmt.Errorf("root base publication does not support prepared parents: %w", errs.ErrInvalidConfig)
		}
	}

	if _, parent := entry.Provenance["parent_base"]; parent {
		return image, entry, fmt.Errorf("root base publication does not support parent lineage: %w", errs.ErrInvalidConfig)
	}

	return image, entry, nil
}

// admitBaseStagePublication admits the stage's images, base images only, and
// projects one ledger entry for each. The plan reader refuses a duplicate
// name, final tag or SBOM path among them. A name is held to the flavor
// rule: it names the image's artifacts and files in every job of the stage.
// A final tag is <base-input-id>-<flavor>, the content-addressed name the
// cleanup and prune verbs read, so no plan promotes a tag of another name.
func admitBaseStagePublication(in PublishRootBaseInput) ([]pipeline.ReleaseImage, []imageledger.Entry, error) {
	for _, image := range in.Plan.Images {
		if !baseImageFlavorRE.MatchString(image.Name) {
			return nil, nil, fmt.Errorf("base image name %q must start with a letter or digit and hold only letters, digits, '.', '_' and '-': %w", image.Name, errs.ErrInvalidConfig)
		}

		if image.ImageKind != string(imageledger.ImageKindBase) {
			return nil, nil, fmt.Errorf("native base stage requires base images only: %w", errs.ErrInvalidConfig)
		}
	}

	body, err := json.Marshal(in.Plan)
	if err != nil {
		return nil, nil, err
	}

	plan, err := pipeline.ReadImageBuildPlan(string(body), in.ExpectedRepository, "")
	if err != nil {
		return nil, nil, err
	}

	entries := make([]imageledger.Entry, 0, len(plan.Images))

	for _, image := range plan.Images {
		if image.BaseRef != "" || image.MovingTagName != "" || image.CacheTagPrefix != "" {
			return nil, nil, fmt.Errorf("native base stage does not support base-ref, moving tags or architecture caches: %w", errs.ErrInvalidConfig)
		}

		if image.FinalTagName != image.BaseInputID+"-"+image.Flavor {
			return nil, nil, fmt.Errorf("base image %q: the final tag %q must be <base-input-id>-<flavor>: %w", image.Name, image.FinalTagName, errs.ErrInvalidConfig)
		}

		entry := imageledger.Entry{
			Role: cmp.Or(image.Role, image.Name), ImageKind: imageledger.ImageKindBase, Flavor: image.Flavor,
			BaseInputID: image.BaseInputID, FinalTag: image.ImageRepository + ":" + image.FinalTagName,
			CandidateTag: image.ImageRepository + ":" + image.CandidateTag,
			SBOM:         imageledger.DefaultSBOMPath(imageledger.ImageKindBase, image.Flavor, image.Role),
		}
		if image.ProvenanceJSON != "" {
			if err := json.Unmarshal([]byte(image.ProvenanceJSON), &entry.Provenance); err != nil {
				return nil, nil, err
			}
		}

		entries = append(entries, entry)
	}

	return plan.Images, entries, nil
}

// ValidateRootBasePlan admits the deliberately limited cold, single-root native
// boundary before scheduling or building. Consumer graphs and IDs remain opaque.
func ValidateRootBasePlan(plan pipeline.ReleaseImagesPlan, repository string) error {
	_, _, err := admitRootBasePublication(PublishRootBaseInput{Plan: plan, ExpectedRepository: repository})

	return err
}

// Resolve existing directory aliases before creating anything. A publisher must
// not write inside consumer layouts, nor contain them in its output payload.
func createRootBaseOutput(in PublishRootBaseInput) error {
	source, err := filepath.EvalSymlinks(in.LayoutsDir)
	if err != nil {
		return err
	}

	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}

	parent, err := filepath.EvalSymlinks(filepath.Dir(in.OutputDir))
	if err != nil {
		return err
	}

	target, err := filepath.Abs(filepath.Join(parent, filepath.Base(in.OutputDir)))
	if err != nil {
		return err
	}

	if source == target || strings.HasPrefix(target, source+string(filepath.Separator)) || strings.HasPrefix(source, target+string(filepath.Separator)) {
		return fmt.Errorf("root base output and layouts must be disjoint: %w", errs.ErrUsage)
	}

	if err := os.Mkdir(in.OutputDir, 0o750); err != nil {
		return err
	}

	for _, name := range []string{"dist", "premade-sboms"} {
		if err := os.Mkdir(filepath.Join(in.OutputDir, name), 0o750); err != nil {
			return err
		}
	}

	return nil
}
