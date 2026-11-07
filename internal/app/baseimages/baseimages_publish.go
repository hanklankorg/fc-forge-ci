// SPDX-FileCopyrightText: The Forge CI Authors
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
// This application primitive does not enable native BASE entry points. Parents,
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

	if registry == nil || in.Trivy == nil || in.Syft == nil || in.Gate == nil {
		return fmt.Errorf("root base publication requires registry, scanners and publication gate: %w", errs.ErrMissingInput)
	}

	if err := appcontainer.AdmitReleaseImageLayouts(in.LayoutsDir, []pipeline.ReleaseImage{image}); err != nil {
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

	evidence := filepath.Join(in.OutputDir, "premade-sboms")
	archs := strings.Fields(image.Archs)
	assembly := rootBaseAssembleInput(in, image, work)
	assembly.Evidence.Stderr = out

	staged, err := appcontainer.AdmitStagedSignerImage(ctx, registry, out, assembly)
	if err != nil {
		return err
	}

	// Pin retained producer bytes before any upload, not a signer-side rescan.
	body, err := cliio.ReadRegularFile(filepath.Join(evidence, "inventory-"+image.Name+"-"+archs[0]+".cyclonedx.json"))
	if err != nil {
		return err
	}

	entry.SBOMSHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	if err := cliio.WriteFile(filepath.Join(in.OutputDir, entry.SBOM), body, 0o644); err != nil {
		return err
	}

	meta, err := staged.Publish(ctx, registry, nil, nil, out)
	if err != nil {
		return err
	}

	entry.Digest, entry.Ref = meta.Digest, meta.Ref

	ledger, err := imageledger.Marshal([]imageledger.Entry{entry})
	if err != nil {
		return err
	}

	return cliio.WriteFile(filepath.Join(in.OutputDir, "dist", "base-images.json"), ledger, 0o644)
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

	body, err := json.Marshal(in.Plan)
	if err != nil {
		return image, entry, err
	}

	plan, err := pipeline.ReadImageBuildPlan(string(body), in.ExpectedRepository, "")
	if err != nil {
		return image, entry, err
	}

	if len(plan.Images) != 1 {
		return image, entry, fmt.Errorf("root base publication requires exactly one image: %w", errs.ErrInvalidConfig)
	}

	image = plan.Images[0]
	if image.ImageKind != string(imageledger.ImageKindBase) || image.BaseRef != "" || image.MovingTagName != "" || image.CacheTagPrefix != "" {
		return image, entry, fmt.Errorf("root base publication requires a base without parent, moving tag or cache: %w", errs.ErrInvalidConfig)
	}

	for _, build := range plan.Builds {
		if len(build.RegistryInputs) != 0 {
			return image, entry, fmt.Errorf("root base publication does not support prepared parents: %w", errs.ErrInvalidConfig)
		}
	}

	entry = imageledger.Entry{
		Role: cmp.Or(image.Role, image.Name), ImageKind: imageledger.ImageKindBase, Flavor: image.Flavor,
		BaseInputID: image.BaseInputID, FinalTag: image.ImageRepository + ":" + image.FinalTagName,
		CandidateTag: image.ImageRepository + ":" + image.CandidateTag,
		SBOM:         imageledger.DefaultSBOMPath(imageledger.ImageKindBase, image.Flavor, image.Role),
	}
	if image.ProvenanceJSON != "" {
		if err := json.Unmarshal([]byte(image.ProvenanceJSON), &entry.Provenance); err != nil {
			return image, entry, err
		}
	}

	if _, parent := entry.Provenance["parent_base"]; parent {
		return image, entry, fmt.Errorf("root base publication does not support parent lineage: %w", errs.ErrInvalidConfig)
	}

	return image, entry, nil
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
