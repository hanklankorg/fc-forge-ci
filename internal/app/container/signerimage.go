// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/git"
	domainrepository "github.com/diggsweden/forge-ci/internal/domain/repository"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
	"github.com/diggsweden/forge-ci/internal/retry"
)

const (
	// defaultMultiarchImageContainerfile and defaultMultiarchImageName are the
	// forge/consumer-neutral defaults; a caller (e.g. a signer image) overrides
	// them with its own Containerfile path and metadata basename.
	defaultMultiarchImageContainerfile = "Containerfile"
	defaultMultiarchImageName          = "image"
	defaultSignerImageContext          = "."
	defaultSignerImageAttempts         = 3
	defaultSignerImageRetryDelay       = 10 * time.Second
)

// SignerImageTool is the buildah and registry surface needed by one architecture
// build. Manifest assembly uses the OCI registry directly.
type SignerImageTool interface {
	BuildSignerImage(ctx context.Context, req domaincontainer.SignerImageBuildToolRequest, out io.Writer) error
	ExportLocalImageToLayout(ctx context.Context, imageRef, layoutDir string, out io.Writer) error
	PushLayout(ctx context.Context, authFile, layoutDir, dest string) (string, error)
	RawManifest(ctx context.Context, authFile, image string, out io.Writer) ([]byte, error)
}

// SignerManifestRegistry checks the immutable image's platform before merging.
// An architecture artifact must name a single image, not a multi-platform index.
type SignerManifestRegistry interface {
	RawManifestRegistry
	MergeManifestWithDigest(ctx context.Context, image string, digests, tags []string) (string, error)
	ImagePlatform(ctx context.Context, ref string) (string, string, error)
	Labels(ctx context.Context, ref string) (map[string]string, error)
}

// SignerImageBuildArchInput drives one architecture build of the signer image.
type SignerImageBuildArchInput struct {
	AuthFile string
	Arch     string
	// HostArch is the GOARCH this engine binary was compiled for. The layer
	// runs only the binary of its host's architecture, so Arch must equal
	// it: that is what refuses a build whose steps would run emulated.
	HostArch         string
	SourceSHA        string
	ServerURL        string
	Repository       string
	RepositorySuffix string // appended to lower(repository) to form the image repo (e.g. "-signer")
	ImageRepository  string // optional full OCI repository; overrides the derived image destination, not source labels
	TagPrefix        string // prepended to the source-SHA image tag (e.g. "signer-")
	TagName          string // explicit index tag; mutually exclusive with TagPrefix
	Name             string // metadata basename shared with assemble; empty defaults to "image"
	Title            string // org.opencontainers.image.title label; empty omits the label
	Containerfile    string
	Context          string
	Target           string   // multi-stage target stage; empty builds the final stage
	BuildArgs        []string // KEY=VALUE build arguments
	Labels           []string // key=value labels beside the source, revision and title
	CacheTag         string
	CacheLabels      []string
	MetadataDir      string
	RetryAttempts    int
	RetryDelay       time.Duration
}

// SignerImageAssembleInput drives multi-arch manifest assembly.
type SignerImageAssembleInput struct {
	// LayoutsDir is the gathered architecture-artifact root; empty preserves cwd.
	LayoutsDir       string
	AuthFile         string
	SourceSHA        string
	ServerURL        string
	Repository       string
	RepositorySuffix string
	ImageRepository  string
	TagPrefix        string
	TagName          string
	RequiredLabels   []string
	CacheTagPrefix   string
	ManifestOutput   string
	Name             string
	Archs            []string
	MetadataDir      string
	RetryAttempts    int
	RetryDelay       time.Duration
	// Evidence, when set, scans every staged architecture before the first
	// upload. Only PublishAndAssembleSignerImage reads it.
	Evidence *StagedLayoutEvidence
}

// StagedLayoutEvidence is the scan a staged architecture passes before
// publication: the Trivy evidence of its layout, then the publication gate.
// The build job may lack the scanner, as the kaniko job image does; the
// publishing job runs it on the very bytes it uploads.
type StagedLayoutEvidence struct {
	// CheckLayout optionally supplies a policy-specific scan/inventory gate.
	// It runs for every verified local layout before the first upload.
	CheckLayout func(context.Context, string, string) error
	Trivy       ImageEvidenceTrivy
	Syft        ImageEvidenceSyft
	Stderr      io.Writer
	// TrivyOutputTemplate names each architecture's Trivy JSON; it must
	// contain {arch} or {platform}.
	TrivyOutputTemplate string
	// SBOMOutputTemplate optionally retains an inventory of every staged child.
	// These exact documents are pinned in the ledger before crossing the signer boundary.
	SBOMOutputTemplate string
	// IndexSBOMOutput binds every generated inventory to the verified index.
	// SBOMURLPrefix is the public directory where those inventories will be served.
	IndexSBOMOutput string
	SBOMURLPrefix   string
	// Gate judges a layout after its evidence is recorded; nil records only.
	Gate func(ctx context.Context, layout, platform string) error
}

// check scans every staged layout, so the evidence of all architectures is
// written, and refuses publication if any of them failed.
func (e *StagedLayoutEvidence) check(ctx context.Context, out io.Writer, inputs []LayoutRecordInput) error {
	if e.CheckLayout != nil {
		var failures []error
		for _, in := range inputs {
			if err := e.CheckLayout(ctx, in.Directory, in.Platform); err != nil {
				failures = append(failures, fmt.Errorf("staged %s: %w", in.Platform, err))
			}
		}
		return errors.Join(failures...)
	}
	raws := make([]string, 0, len(inputs))
	for _, in := range inputs {
		raws = append(raws, in.Platform)
	}

	platforms, err := parseImageEvidencePlatforms(raws)
	if err != nil {
		return err
	}

	if err := validateImageEvidenceOutputTemplate(e.TrivyOutputTemplate, "trivy output template", platforms); err != nil {
		return err
	}

	if e.SBOMOutputTemplate != "" {
		if err := validateImageEvidenceOutputTemplate(e.SBOMOutputTemplate, "sbom output template", platforms); err != nil {
			return err
		}
	}

	var failures []error

	for idx, in := range inputs {
		platform := platforms[idx]

		err := ImageEvidence(ctx, nil, nil, e.Trivy, e.Syft, out, e.Stderr, ImageEvidenceInput{
			OCILayout:   in.Directory,
			TrivyOutput: renderImageEvidenceTemplate(e.TrivyOutputTemplate, platform),
			SBOMOutput:  renderImageEvidenceTemplate(e.SBOMOutputTemplate, platform),
		})
		if err == nil && e.Gate != nil {
			err = e.Gate(ctx, in.Directory, platform.Raw)
		}

		if err != nil {
			failures = append(failures, fmt.Errorf("staged %s: %w", platform.Raw, err))
		}
	}

	return errors.Join(failures...)
}

// SignerImageArchMetadata is the JSON artifact uploaded by each arch job.
type SignerImageArchMetadata struct {
	Arch   string `json:"arch"`
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
	Ref    string `json:"ref"`
}

// SignerImageMetadata is the JSON artifact uploaded by the manifest assembly
// job.
type SignerImageMetadata struct {
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
	Ref    string `json:"ref"`
}

// BuildSignerImageArch builds and pushes one signer image architecture, writes
// signer-image-<arch>.json, and returns its metadata.
func BuildSignerImageArch(ctx context.Context, tool SignerImageTool, out io.Writer, in SignerImageBuildArchInput) (*SignerImageArchMetadata, error) {
	return buildSignerImageArch(ctx, tool, nil, out, in)
}

// StageSignerImageArch retains the architecture layout and metadata for a job
// depending on the complete architecture matrix. It performs no registry writes.
func StageSignerImageArch(ctx context.Context, tool SignerImageTool, inspector LayoutInspector, out io.Writer, in SignerImageBuildArchInput) (*SignerImageArchMetadata, error) {
	if inspector == nil {
		return nil, fmt.Errorf("staging requires a local layout inspector: %w", errs.ErrUsage)
	}

	return buildSignerImageArch(ctx, tool, inspector, out, in)
}

func buildSignerImageArch(ctx context.Context, tool SignerImageTool, inspector LayoutInspector, out io.Writer, in SignerImageBuildArchInput) (*SignerImageArchMetadata, error) {
	// Staging writes no registry, so a build that pulls only public bases
	// needs no credentials; a push does.
	derived, err := deriveSignerImageArch(in, inspector == nil)
	if err != nil {
		return nil, err
	}

	if in.AuthFile == "" {
		anonymous, cleanup, authErr := anonymousAuthFile()
		if authErr != nil {
			return nil, authErr
		}
		defer cleanup()

		in.AuthFile = anonymous
	}

	if err = recreateSignerMetadataDir(derived.MetadataDir); err != nil {
		return nil, err
	}

	cached, cacheErr := stageCachedArchitecture(ctx, inspector, out, in, derived)
	if !errors.Is(cacheErr, errArchitectureCacheMiss) {
		return cached, cacheErr
	}

	if err = tool.BuildSignerImage(ctx, domaincontainer.SignerImageBuildToolRequest{
		AuthFile:      in.AuthFile,
		Platform:      derived.Platform,
		SourceURL:     derived.SourceURL,
		Revision:      in.SourceSHA,
		LocalImage:    derived.LocalImage,
		Containerfile: derived.Containerfile,
		Context:       derived.Context,
		Title:         in.Title,
		Target:        in.Target,
		BuildArgs:     in.BuildArgs,
		Labels:        in.Labels,
	}, out); err != nil {
		return nil, err
	}

	var digest string
	if inspector == nil {
		digest, err = publishSignerArchitecture(ctx, tool, out, in, derived)
	} else {
		digest, err = stageSignerArchitecture(ctx, tool, inspector, out, in, derived)
	}

	if err != nil {
		return nil, err
	}

	meta := &SignerImageArchMetadata{
		Arch:   in.Arch,
		Tag:    derived.ImageTag,
		Digest: digest,
		Ref:    derived.ImageRepository + "@" + digest,
	}

	if err := writeSignerJSON(filepath.Join(derived.MetadataDir, derived.Name+"-"+in.Arch+".json"), meta); err != nil {
		return nil, err
	}

	_, _ = fmt.Fprintf(out, "Image architecture prepared: arch=%s digest=%s\n", in.Arch, digest)

	return meta, nil
}

func stageSignerArchitecture(ctx context.Context, tool SignerImageTool, inspector LayoutInspector, out io.Writer, in SignerImageBuildArchInput, derived signerImageArchDerived) (string, error) {
	directory := filepath.Join(derived.MetadataDir, "layout")
	if err := tool.ExportLocalImageToLayout(ctx, derived.LocalImage, directory, out); err != nil {
		return "", err
	}

	record, err := RecordImageLayout(ctx, inspector, LayoutRecordInput{Directory: directory, ImageRef: derived.ImageTag, Platform: derived.Platform, SourceSHA: in.SourceSHA})
	if err != nil {
		return "", err
	}

	return record.Digest, nil
}

// StagedSignerRegistry supports the deferred upload and existing assembly path.
type StagedSignerRegistry interface {
	LayoutRegistry
	SignerManifestRegistry
}

// PublishAndAssembleSignerImage admits all architecture records and local blobs
// before uploading any of them, then retains the existing manifest/output contract.
func PublishAndAssembleSignerImage(ctx context.Context, registry StagedSignerRegistry, sink ci.OutputSink, summary ci.SummarySink, out io.Writer, in SignerImageAssembleInput) (*SignerImageMetadata, error) {
	derived, err := deriveSignerImageManifest(in)
	if err != nil {
		return nil, err
	}

	if cacheErr := validateArchitectureCachePublication(in, derived); cacheErr != nil {
		return nil, cacheErr
	}

	inputs, err := stagedSignerInputs(ctx, registry, in, derived)
	if err != nil {
		return nil, err
	}
	// Establish source/platform/content binding before policy callbacks inspect
	// inputs. PublishImageLayouts rechecks the same bytes after the callbacks.
	if _, err := verifyImageLayouts(ctx, registry, inputs); err != nil {
		return nil, err
	}

	if in.Evidence != nil {
		if err := validateStagedIndexEvidence(in, derived, inputs); err != nil {
			return nil, err
		}

		if scanErr := in.Evidence.check(ctx, out, inputs); scanErr != nil {
			return nil, scanErr
		}
	}

	if _, uploadErr := PublishImageLayouts(ctx, registry, out, inputs, in.RetryAttempts, in.RetryDelay); uploadErr != nil {
		return nil, uploadErr
	}

	meta, err := AssembleSignerImageManifest(ctx, registry, sink, summary, out, in)
	if err != nil {
		return nil, err
	}

	if in.Evidence != nil && in.Evidence.IndexSBOMOutput != "" {
		if err := writeStagedIndexEvidence(ctx, sink, in, inputs, meta); err != nil {
			return nil, err
		}
	}

	if cacheErr := publishArchitectureCaches(ctx, registry, out, in, derived, inputs); cacheErr != nil {
		return nil, cacheErr
	}

	return meta, nil
}

func stagedSignerInputs(ctx context.Context, registry LayoutInspector, in SignerImageAssembleInput, derived signerImageManifestDerived) ([]LayoutRecordInput, error) {
	inputs := make([]LayoutRecordInput, 0, len(derived.Archs))
	for _, arch := range derived.Archs {
		tag := derived.ManifestTag + "-" + arch

		ref, err := signerArchRef(in.LayoutsDir, arch, derived.ImageRepository, derived.Name, tag)
		if err != nil {
			return nil, err
		}

		directory := filepath.Join(in.LayoutsDir, derived.Name+"-arch-"+arch, "layout")

		record, err := readImageLayoutRecord(directory)
		if err != nil {
			return nil, err
		}

		if ref != derived.ImageRepository+"@"+record.Digest {
			return nil, fmt.Errorf("architecture metadata differs from staged layout: %w", errs.ErrValidation)
		}

		if err := matchStagedMultiarchLabels(ctx, registry, directory, record.Digest, in.RequiredLabels); err != nil {
			return nil, err
		}

		inputs = append(inputs, LayoutRecordInput{Directory: directory, ImageRef: tag, Platform: "linux/" + arch, SourceSHA: in.SourceSHA})
	}

	return inputs, nil
}

// AssembleSignerImageManifest assembles the per-arch signer images into the
// final multi-arch manifest, writes signer-image.json, and emits CI outputs.
func AssembleSignerImageManifest(ctx context.Context, registry SignerManifestRegistry, sink ci.OutputSink, summary ci.SummarySink, out io.Writer, in SignerImageAssembleInput) (*SignerImageMetadata, error) {
	derived, err := prepareSignerImageManifest(ctx, registry, in)
	if err != nil {
		return nil, err
	}

	if err = recreateSignerMetadataDir(derived.MetadataDir); err != nil {
		return nil, err
	}

	mergedDigest, err := retry.Do(ctx, out, retry.Attempts(in.RetryAttempts, defaultSignerImageAttempts), retry.Delay(in.RetryDelay, defaultSignerImageRetryDelay), func() (string, error) {
		return registry.MergeManifestWithDigest(ctx, derived.ImageRepository, derived.Digests, []string{derived.ManifestTag})
	})
	if err != nil {
		return nil, fmt.Errorf("failed to push signer image manifest: %w", err)
	}

	raw, err := registry.Manifest(ctx, derived.ManifestTag)
	if err != nil {
		return nil, err
	}

	digest, err := digestRawManifest(raw, derived.ManifestTag)
	if err != nil {
		return nil, err
	}

	if digest != mergedDigest {
		return nil, fmt.Errorf("merged signer index does not match the registry manifest: %w", errs.ErrValidation)
	}

	if err = writeVerifiedIndex(in.ManifestOutput, raw); err != nil {
		return nil, err
	}

	meta := &SignerImageMetadata{
		Tag:    derived.ManifestTag,
		Digest: digest,
		Ref:    derived.ImageRepository + "@" + digest,
	}

	if err = writeSignerJSON(filepath.Join(derived.MetadataDir, derived.Name+".json"), meta); err != nil {
		return nil, err
	}

	if err = emitSignerImageManifestOutputs(ctx, sink, summary, meta); err != nil {
		return nil, err
	}

	_, _ = fmt.Fprintf(out, "Image manifest pushed: %s\n", meta.Ref)

	return meta, nil
}

// publishSignerArchitecture owns one export through all push attempts and the
// served-digest check. Metadata is published only after this scope succeeds.
func publishSignerArchitecture(ctx context.Context, tool SignerImageTool, out io.Writer, in SignerImageBuildArchInput, derived signerImageArchDerived) (string, error) {
	layoutDir, err := os.MkdirTemp("", "forge-ci-signer-layout-*")
	if err != nil {
		return "", fmt.Errorf("create signer image layout: %w", err)
	}
	defer func() { _ = os.RemoveAll(layoutDir) }()

	if exportErr := tool.ExportLocalImageToLayout(ctx, derived.LocalImage, layoutDir, out); exportErr != nil {
		return "", exportErr
	}

	pushedDigest, err := retry.Do(ctx, out, retry.Attempts(in.RetryAttempts, defaultSignerImageAttempts), retry.Delay(in.RetryDelay, defaultSignerImageRetryDelay), func() (string, error) {
		return tool.PushLayout(ctx, in.AuthFile, layoutDir, derived.ImageTag)
	})
	if err != nil {
		return "", fmt.Errorf("failed to push signer image architecture: %w", err)
	}

	raw, err := tool.RawManifest(ctx, in.AuthFile, derived.ImageTag, out)
	if err != nil {
		return "", err
	}

	digest, err := digestRawManifest(raw, derived.ImageTag)
	if err != nil {
		return "", err
	}

	if digest != pushedDigest {
		return "", fmt.Errorf("pushed signer layout does not match the registry manifest: %w", errs.ErrValidation)
	}

	return digest, nil
}

// emitSignerImageManifestOutputs publishes the assembled manifest metadata to
// the CI output and summary sinks.
func emitSignerImageManifestOutputs(ctx context.Context, sink ci.OutputSink, summary ci.SummarySink, meta *SignerImageMetadata) error {
	if sink != nil {
		if err := sink.Set(ctx, "image-ref", meta.Ref); err != nil {
			return err
		}

		if err := sink.Set(ctx, "image-digest", meta.Digest); err != nil {
			return err
		}

		if err := sink.Set(ctx, "image-tag", meta.Tag); err != nil {
			return err
		}
	}

	if summary != nil {
		if err := summary.Append(ctx, fmt.Sprintf("### Image\n\n* Tag: `%s`\n* Digest: `%s`\n* Ref: `%s`\n", meta.Tag, meta.Digest, meta.Ref)); err != nil {
			return fmt.Errorf("append signer image summary: %w", err)
		}
	}

	return nil
}

type signerImageArchDerived struct {
	ImageRepository string
	ImageTag        string
	LocalImage      string
	MetadataDir     string
	Name            string
	Platform        string
	SourceURL       string
	Containerfile   string
	Context         string
}

type signerImageManifestDerived struct {
	ImageRepository string
	ManifestTag     string
	Name            string
	MetadataDir     string
	Archs           []string
	Digests         []string
}

func deriveSignerImageArch(in SignerImageBuildArchInput, authRequired bool) (signerImageArchDerived, error) {
	if err := validateSignerMetadataName(in.Name); err != nil {
		return signerImageArchDerived{}, err
	}

	if err := validateSignerImageCommon(in.AuthFile, authRequired, in.SourceSHA, in.ServerURL, in.Repository); err != nil {
		return signerImageArchDerived{}, err
	}

	if err := validateSignerArch(in.Arch); err != nil {
		return signerImageArchDerived{}, err
	}

	if err := domaincontainer.RequireNativeArch(in.Arch, in.HostArch); err != nil {
		return signerImageArchDerived{}, err
	}

	imageRepo, err := signerImageRepository(in.ServerURL, in.Repository, in.RepositorySuffix, in.ImageRepository)
	if err != nil {
		return signerImageArchDerived{}, err
	}

	name := defaultSignerString(in.Name, defaultMultiarchImageName)
	containerfile := defaultSignerString(in.Containerfile, defaultMultiarchImageContainerfile)
	contextDir := defaultSignerString(in.Context, defaultSignerImageContext)
	metadataDir := defaultSignerString(in.MetadataDir, name+"-arch-"+in.Arch)

	tag, err := multiarchImageTag(imageRepo, in.SourceSHA, in.TagPrefix, in.TagName)
	if err != nil {
		return signerImageArchDerived{}, err
	}

	return signerImageArchDerived{
		ImageRepository: imageRepo,
		ImageTag:        tag + "-" + in.Arch,
		LocalImage:      fmt.Sprintf("%s:%s-%s", localScratchImage(name), in.SourceSHA, in.Arch),
		MetadataDir:     metadataDir,
		Name:            name,
		Platform:        "linux/" + in.Arch,
		SourceURL:       strings.TrimRight(in.ServerURL, "/") + "/" + in.Repository,
		Containerfile:   containerfile,
		Context:         contextDir,
	}, nil
}

// signerManifestArchs is the requested architectures, both by default, each
// supported and named once.
func signerManifestArchs(archs []string) ([]string, error) {
	if len(archs) == 0 {
		return []string{domaincontainer.ArchAMD64, domaincontainer.ArchARM64}, nil
	}

	seen := make(map[string]bool, len(archs))
	for _, arch := range archs {
		if err := validateSignerArch(arch); err != nil {
			return nil, err
		}

		if seen[arch] {
			return nil, fmt.Errorf("duplicate requested architecture %s: %w", arch, errs.ErrValidation)
		}

		seen[arch] = true
	}

	return archs, nil
}

func deriveSignerImageManifest(in SignerImageAssembleInput) (signerImageManifestDerived, error) {
	if err := validateSignerMetadataName(in.Name); err != nil {
		return signerImageManifestDerived{}, err
	}

	if err := validateSignerImageCommon(in.AuthFile, true, in.SourceSHA, in.ServerURL, in.Repository); err != nil {
		return signerImageManifestDerived{}, err
	}

	archs, err := signerManifestArchs(in.Archs)
	if err != nil {
		return signerImageManifestDerived{}, err
	}

	imageRepo, err := signerImageRepository(in.ServerURL, in.Repository, in.RepositorySuffix, in.ImageRepository)
	if err != nil {
		return signerImageManifestDerived{}, err
	}

	name := defaultSignerString(in.Name, defaultMultiarchImageName)

	tag, err := multiarchImageTag(imageRepo, in.SourceSHA, in.TagPrefix, in.TagName)
	if err != nil {
		return signerImageManifestDerived{}, err
	}

	if _, err = parseRequiredImageLabels(in.RequiredLabels); err != nil {
		return signerImageManifestDerived{}, err
	}

	derived := signerImageManifestDerived{
		ImageRepository: imageRepo,
		ManifestTag:     tag,
		MetadataDir:     defaultSignerString(in.MetadataDir, name+"-dist"),
		Name:            name,
		Archs:           archs,
	}
	if err = validateIndexSnapshotPath(in.ManifestOutput, in.AuthFile, derived); err != nil {
		return signerImageManifestDerived{}, err
	}

	return derived, nil
}

func validateSignerImageCommon(authFile string, authRequired bool, sourceSHA, serverURL, repository string) error {
	if authFile != "" || authRequired {
		if err := requireSingleLine(authFile, "auth-file"); err != nil {
			return err
		}

		if _, err := os.Stat(authFile); err != nil {
			return fmt.Errorf("auth-file not found: %s: %w", authFile, err)
		}
	}

	if !git.ValidCommitSHA(sourceSHA) {
		return fmt.Errorf("SOURCE_SHA must be a git commit hex digest: %w", errs.ErrValidation)
	}

	if err := requireSingleLine(serverURL, "server-url"); err != nil {
		return err
	}

	// Check the original source before a suffix can turn an incomplete path
	// (e.g. "owner/" + "image") into a valid-looking image repository.
	_, err := domainrepository.Parse(repository)

	return err
}

func validateSignerMetadataName(name string) error {
	if name != "" && (name == "." || !pathsafe.Relative(name) || filepath.Base(name) != name || strings.ContainsAny(name, "\\\r\n")) {
		return fmt.Errorf("metadata name must be a plain basename: %w", errs.ErrUsage)
	}

	return nil
}

func validateSignerArch(arch string) error {
	switch arch {
	case domaincontainer.ArchAMD64, domaincontainer.ArchARM64:
		return nil
	default:
		return fmt.Errorf("unsupported signer architecture: %s: %w", arch, errs.ErrValidation)
	}
}

func signerImageRepository(serverURL, repository, suffix, imageRepository string) (string, error) {
	registryHost, err := domaincontainer.RegistryHost(serverURL)
	if err != nil {
		return "", err
	}

	if imageRepository != "" {
		if !domaincontainer.ValidRepositoryName(imageRepository) {
			return "", fmt.Errorf("image-repository must be a lowercase OCI repository with an explicit registry and no tag or digest: %w", errs.ErrUsage)
		}

		return imageRepository, nil
	}

	return DefaultReleaseImageRepository(registryHost, repository+suffix)
}

// localScratchImage is the throwaway local buildah tag used while building;
// it never leaves the runner. It is derived from the metadata name
// so a non-signer multiarch build isn't tagged "signer".
func localScratchImage(name string) string {
	return "localhost/" + name
}

func signerArchRef(root, arch, imageRepository, name, expectedTag string) (string, error) {
	metadataFile := filepath.Join(root, name+"-arch-"+arch, name+"-"+arch+".json")

	body, err := os.ReadFile(metadataFile) //nolint:gosec // workspace-local metadata artifact downloaded by the workflow.
	if err != nil {
		return "", fmt.Errorf("image metadata missing: %s: %w", metadataFile, err)
	}

	var meta SignerImageArchMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return "", fmt.Errorf("parse image metadata %s: %w: %w", metadataFile, err, errs.ErrInvalidConfig)
	}

	ref := meta.Ref
	if !domaincontainer.ValidDigestPinnedRef(ref) {
		return "", fmt.Errorf("image ref must be digest-pinned: %s: %w", ref, errs.ErrValidation)
	}

	if !strings.HasPrefix(ref, imageRepository+"@sha256:") {
		return "", fmt.Errorf("image ref must be under %s: %s: %w", imageRepository, ref, errs.ErrValidation)
	}

	if meta.Arch != arch || meta.Tag != expectedTag || !domaincontainer.ValidDigest(meta.Digest) || ref != imageRepository+"@"+meta.Digest {
		return "", fmt.Errorf("image metadata architecture, tag, digest and ref must agree: %w", errs.ErrValidation)
	}

	return ref, nil
}

// digestRawManifest is the content digest of the manifest the registry
// served. The digest names what every later step signs and verifies, so a
// body that cannot be a manifest -- empty, or not a JSON object -- is refused
// rather than hashed into a reference to nothing.
func digestRawManifest(raw []byte, image string) (string, error) {
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest == nil {
		return "", fmt.Errorf("registry returned no manifest document for %s: %w", image, errs.ErrMalformedInput)
	}

	sum := sha256.Sum256(raw)

	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func writeSignerJSON(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal signer image metadata: %w", err)
	}

	body = append(body, '\n')
	if err := cliio.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write signer image metadata %s: %w", path, err)
	}

	return nil
}

func recreateSignerMetadataDir(path string) error {
	if !pathsafe.Relative(path) || filepath.Clean(path) == "." || strings.Contains(path, "\\") {
		return fmt.Errorf("metadata-dir must be a safe relative directory: %s: %w", path, errs.ErrUsage)
	}

	root, err := pathsafe.MkdirRoot(filepath.Dir(path), 0o755)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	base := filepath.Base(path)
	if info, err := root.Lstat(base); err == nil && !info.IsDir() {
		return fmt.Errorf("metadata directory must be a real directory: %w", errs.ErrValidation)
	}

	if err := root.RemoveAll(base); err != nil {
		return fmt.Errorf("remove signer image metadata dir %s: %w", path, err)
	}

	if err := root.Mkdir(base, 0o755); err != nil {
		return fmt.Errorf("create signer image metadata dir %s: %w", path, err)
	}

	return nil
}

// anonymousAuthFile is an auth file with no credentials, so a staged build
// pulls anonymously instead of with whatever registry login a persistent
// runner keeps in the builder's default auth file.
func anonymousAuthFile() (string, func(), error) {
	file, err := os.CreateTemp("", "forge-ci-anonymous-auth-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("create anonymous registry auth file: %w", err)
	}

	cleanup := func() { _ = os.Remove(file.Name()) }

	_, err = file.WriteString(`{"auths":{}}`)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		cleanup()

		return "", nil, fmt.Errorf("write anonymous registry auth file: %w", err)
	}

	return file.Name(), cleanup, nil
}

func requireSingleLine(value, label string) error {
	if value == "" || strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("%s must be a non-empty single-line value: %w", label, errs.ErrUsage)
	}

	return nil
}

func defaultSignerString(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}
