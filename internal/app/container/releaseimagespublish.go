// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/imageledger"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	domainsbom "github.com/diggsweden/forge-ci/internal/domain/sbom"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

// ReleaseImagesLedgerName is the ledger PublishReleaseImages writes in its
// output directory.
const ReleaseImagesLedgerName = "release-images.json"

// PublishReleaseImagesInput is one release-images publish job: the plan, the
// layouts the build jobs staged, and where the ledger and evidence go.
type PublishReleaseImagesInput struct {
	Plan pipeline.ReleaseImagesPlan
	// LayoutsDir holds the build jobs' <name>-arch-<arch> layouts. They ran
	// consumer code, so the directory is read only after it is admitted, and
	// never written.
	LayoutsDir string
	// LayoutSources binds each native job's extracted root to its architecture.
	// Exclusive with LayoutsDir; roots are admitted/read directly, never merged.
	LayoutSources map[string]string
	// OutputDir receives the ledger, the scan results and the index
	// manifests. It must not exist yet.
	OutputDir     string
	AuthFile      string
	ReleaseTag    string
	ReleaseSHA    string
	ServerURL     string
	Repository    string
	RetryAttempts int
	Trivy         ImageEvidenceTrivy
	Syft          ImageEvidenceSyft
	// Opt-in preserves existing native release-image publication behavior.
	PrepublicationSBOMs bool
	SBOMURLPrefix       string
	// Gate judges each scanned layout before any upload.
	Gate func(ctx context.Context, layout, platform string) error
}

// PublishReleaseImages publishes every planned image from its staged layouts
// and writes their ledger to OutputDir. It admits, verifies, scans and gates
// the layouts of all images first and pushes only when every one passes. The
// ledger is built in memory: nothing under LayoutsDir is appended to or
// trusted beyond the planned layouts. With PrepublicationSBOMs the platform
// inventories come from these local layouts, not a later signer rescan. The
// release index BOM binds them to the exact served index, as in Forgejo.
func PublishReleaseImages(ctx context.Context, registry StagedSignerRegistry, summary ci.SummarySink, out io.Writer, in PublishReleaseImagesInput) error {
	if err := validateReleaseImagePublication(in); err != nil {
		return err
	}
	if _, err := os.Lstat(in.OutputDir); err == nil {
		return fmt.Errorf("release-images publish: %s already exists: %w", in.OutputDir, errs.ErrUsage)
	}

	if len(in.LayoutSources) != 0 {
		if in.LayoutsDir != "" {
			return fmt.Errorf("use layouts-dir or layout-source, not both: %w", errs.ErrUsage)
		}

		if err := AdmitReleaseImageLayoutSources(in.LayoutSources, in.Plan.Images); err != nil {
			return err
		}
	} else if err := AdmitReleaseImageLayouts(in.LayoutsDir, in.Plan.Images); err != nil {
		return err
	}

	if err := os.MkdirAll(in.OutputDir, 0o750); err != nil {
		return fmt.Errorf("release-images publish: create %s: %w", in.OutputDir, err)
	}

	// The index metadata assembly writes is workspace-relative and of no use
	// after this job; it stays in a private directory of the job's own.
	work, err := os.MkdirTemp(".", ".forge-ci-release-images-")
	if err != nil {
		return fmt.Errorf("release-images publish: create work directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	// Every image passes its gate before any is pushed: a later image's
	// finding must not leave earlier ones in the registry.
	staged := make([]*StagedSignerImage, 0, len(in.Plan.Images))

	for _, image := range in.Plan.Images {
		admitted, err := AdmitStagedSignerImage(ctx, registry, out, releaseImageAssembleInput(out, in, image, work))
		if err != nil {
			return fmt.Errorf("release image %s: %w", image.Name, err)
		}

		staged = append(staged, admitted)
	}

	ledger, err := imageledger.Marshal(nil)
	if err != nil {
		return err
	}

	for index, image := range in.Plan.Images {
		meta, err := staged[index].Publish(ctx, registry, nil, summary, out)
		if err != nil {
			return fmt.Errorf("release image %s: %w", image.Name, err)
		}

		entry := releaseImageLedgerEntry(in, image, meta.Digest)
		if in.PrepublicationSBOMs {
			body, readErr := cliio.ReadRegularFile(releaseImageSBOMFile(in, image))
			if readErr != nil {
				return readErr
			}
			entry.SBOMSHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
		}
		ledger, err = AppendLedgerEntry(ledger, entry)
		if err != nil {
			return fmt.Errorf("release image %s: %w", image.Name, err)
		}
	}

	path := filepath.Join(in.OutputDir, ReleaseImagesLedgerName)
	if err := cliio.WriteFile(path, ledger, 0o644); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "ledger: %d release image(s) recorded → %s\n", len(in.Plan.Images), path)

	return nil
}

// releaseImageAssembleInput is the staged-layout publication of one planned
// image, its scans and gate run before any upload.
func releaseImageAssembleInput(out io.Writer, in PublishReleaseImagesInput, image pipeline.ReleaseImage, work string) SignerImageAssembleInput {
	revision := cmp.Or(image.Revision, in.ReleaseSHA)
	evidence := &StagedLayoutEvidence{
		Trivy: in.Trivy, Syft: in.Syft, Stderr: out,
		TrivyOutputTemplate: filepath.Join(in.OutputDir, "trivy-results-"+image.Name+"-{arch}.json"), Gate: in.Gate,
	}
	if in.PrepublicationSBOMs {
		id := cmp.Or(image.Flavor, image.Role, image.Name)
		evidence.SBOMOutputTemplate = filepath.Join(in.OutputDir, "sbom-"+id+"-{arch}.json")
		evidence.IndexSBOMOutput = releaseImageSBOMFile(in, image)
		evidence.SBOMURLPrefix = in.SBOMURLPrefix
	}

	return SignerImageAssembleInput{
		LayoutsDir:      in.LayoutsDir,
		LayoutRoots:     in.LayoutSources,
		AuthFile:        in.AuthFile,
		SourceSHA:       revision,
		ServerURL:       in.ServerURL,
		Repository:      in.Repository,
		ImageRepository: image.ImageRepository,
		TagName:         image.CandidateTag,
		RequiredLabels:  append([]string{"org.opencontainers.image.revision=" + revision}, nonEmptyLines(image.RequiredLabels)...),
		CacheTagPrefix:  image.CacheTagPrefix,
		ManifestOutput:  filepath.Join(in.OutputDir, "index-"+image.Name+".json"),
		Name:            image.Name,
		Archs:           nonEmptyLines(image.Archs),
		MetadataDir:     filepath.Join(work, image.Name+"-index"),
		RetryAttempts:   in.RetryAttempts,
		Evidence:        evidence,
	}
}

func releaseImageSBOMFile(in PublishReleaseImagesInput, image pipeline.ReleaseImage) string {
	// The existing native collector maps OutputDir/<file> to dist/<file>,
	// which is then staged at dist/<file> for the signer. Admission below
	// permits exactly that mapping, not arbitrary ledger paths flattened by
	// basename (in particular never premade-sboms/<file>).
	return filepath.Join(in.OutputDir, strings.TrimPrefix(releaseImageCanonicalSBOM(image), "dist/"))
}

func releaseImageCanonicalSBOM(image pipeline.ReleaseImage) string {
	return imageledger.DefaultSBOMPath(imageledger.ImageKind(cmp.Or(image.ImageKind, "release")), image.Flavor, cmp.Or(image.Role, image.Name))
}

// ValidateNativeReleaseImagesPlan retains the base refusal until native jobs
// carry a separate base ledger and its canonical premade-sboms/ evidence through
// the dist-only handoff and the trusted base-images import/sign path.
func ValidateNativeReleaseImagesPlan(plan pipeline.ReleaseImagesPlan) error {
	for _, image := range plan.Images {
		if image.ImageKind == string(imageledger.ImageKindBase) {
			return fmt.Errorf("release image %s: native jobs do not support the base ledger and canonical premade-sboms handoff; use the base-images lifecycle: %w", image.Name, errs.ErrInvalidConfig)
		}
	}
	return nil
}

// Validate ledger policy and all generated filenames before scans or uploads,
// including the collector's canonical-path mapping and cross-image collisions.
func validateReleaseImagePublication(in PublishReleaseImagesInput) error {
	if err := ValidateNativeReleaseImagesPlan(in.Plan); err != nil {
		return err
	}
	if in.PrepublicationSBOMs {
		if err := domainsbom.ValidatePublicBOMURL(in.SBOMURLPrefix); err != nil {
			return err
		}
	} else if in.SBOMURLPrefix != "" {
		return fmt.Errorf("sbom-url-prefix requires prepublication-sboms: %w", errs.ErrUsage)
	}
	seen := map[string]bool{}
	ledger, err := imageledger.Marshal(nil)
	if err != nil {
		return err
	}
	for _, image := range in.Plan.Images {
		if image.CandidateTag != "staging-"+image.FinalTagName {
			return fmt.Errorf("release image candidate tag must match its ledger destination: %w", errs.ErrInvalidConfig)
		}
		if err := validateSignerMetadataName(image.Name); err != nil {
			return err
		}
		if len(nonEmptyLines(image.Archs)) == 0 {
			return fmt.Errorf("release image requires architectures: %w", errs.ErrValidation)
		}
		if _, err := signerManifestArchs(nonEmptyLines(image.Archs)); err != nil {
			return err
		}
		entry := releaseImageLedgerEntry(in, image, "sha256:"+strings.Repeat("0", 64))
		// Duplicate placeholder digests are legitimate for different roles.
		ledger, err = AppendLedgerEntry(ledger, entry)
		if err != nil {
			return err
		}
		if in.PrepublicationSBOMs {
			canonical := releaseImageCanonicalSBOM(image)
			relative, ok := strings.CutPrefix(canonical, "dist/")
			if !ok || relative == "" || filepath.Base(relative) != relative {
				return fmt.Errorf("release image SBOM has no canonical dist handoff mapping: %w", errs.ErrInvalidConfig)
			}
			file := releaseImageSBOMFile(in, image)
			if seen[file] {
				return fmt.Errorf("release image SBOM output collision: %w", errs.ErrUsage)
			}
			seen[file] = true
		}
	}
	return nil
}

// releaseImageLedgerEntry records one pushed image. The publication caller
// adds its SBOM byte pin when inventories were requested.
func releaseImageLedgerEntry(in PublishReleaseImagesInput, image pipeline.ReleaseImage, digest string) AddLedgerEntryInput {
	return AddLedgerEntryInput{
		ReleaseTag:         in.ReleaseTag,
		Role:               cmp.Or(image.Role, image.Name),
		ImageKind:          cmp.Or(image.ImageKind, string(imageledger.ImageKindRelease)),
		ImageName:          image.ImageRepository,
		Digest:             digest,
		ProvenanceJSON:     image.ProvenanceJSON,
		DefaultSBOM:        true,
		FinalTagName:       image.FinalTagName,
		MovingTagName:      image.MovingTagName,
		Flavor:             image.Flavor,
		DeriveCandidateTag: true,
		BaseRef:            image.BaseRef,
		BaseInputID:        image.BaseInputID,
	}
}

// AdmitReleaseImageLayouts admits the directory the build jobs' layouts were
// gathered into. Those jobs ran consumer Containerfiles, so it must hold
// exactly the planned <name>-arch-<arch> directories, each with its metadata
// file and its layout directory, and nothing else: an unplanned entry, a
// missing one or a link is refused before anything is read or uploaded.
func AdmitReleaseImageLayouts(dir string, images []pipeline.ReleaseImage) error {
	root, err := pathsafe.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("release image layouts %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()

	// Each planned layout directory, and the entries it holds by type.
	want := map[string]map[string]fs.FileMode{}

	for _, image := range images {
		for _, arch := range nonEmptyLines(image.Archs) {
			want[image.Name+"-arch-"+arch] = map[string]fs.FileMode{
				image.Name + "-" + arch + ".json": 0,
				"layout":                          fs.ModeDir,
			}
		}
	}

	dirs := map[string]fs.FileMode{}
	for name := range want {
		dirs[name] = fs.ModeDir
	}

	if err := admitEntries(root.FS(), ".", dirs); err != nil {
		return fmt.Errorf("release image layouts %s: %w", dir, err)
	}

	for name, entries := range want {
		if err := admitEntries(root.FS(), name, entries); err != nil {
			return fmt.Errorf("release image layouts %s: %w", dir, err)
		}
	}

	return nil
}

// admitEntries requires dir to hold exactly the entries of want, each of the
// type it gives, and nothing else. A link is never of a wanted type.
func admitEntries(fsys fs.FS, dir string, want map[string]fs.FileMode) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		name := path.Join(dir, entry.Name())

		mode, ok := want[entry.Name()]
		if !ok {
			return fmt.Errorf("%s is not a planned layout entry: %w", name, errs.ErrValidation)
		}

		if entry.Type() != mode {
			return fmt.Errorf("%s is a link or of another type than its layout entry: %w", name, errs.ErrValidation)
		}
	}

	if len(entries) != len(want) {
		return fmt.Errorf("%s lacks a planned layout entry: %w", dir, errs.ErrValidation)
	}

	return nil
}
