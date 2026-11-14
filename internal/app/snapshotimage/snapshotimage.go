// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package snapshotimage implements the shared snapshot-image boundary.
package snapshotimage

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	appsecurity "github.com/diggsweden/forge-ci/internal/app/security"
	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/domain/ci"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

var (
	errHeadDiffers            = errors.New("differs from source-sha")
	errOutputNotRegular       = errors.New("output must name a regular file")
	errJSONOutputNotRegular   = errors.New("snapshot JSON output must be a regular file")
	errNativeRunner           = errors.New("requires its native runner")
	errMetadataDirRelative    = errors.New("snapshot build metadata-dir must be workspace-relative")
	errUnplannedBuild         = errors.New("unplanned snapshot build")
	errEvidenceDirRelative    = errors.New("snapshot evidence-dir must be workspace-relative")
	errScanRequiresTrivy      = errors.New("snapshot scan policy requires Trivy")
	errSBOMRequiresSyft       = errors.New("snapshot SBOM policy requires Syft")
	errInvalidCycloneDX       = errors.New("invalid snapshot CycloneDX inventory")
	errUnexpectedReceiptEntry = errors.New("unexpected snapshot receipt entry")
)

// PlanInput selects the configuration file, logical branch, source commit and
// image repository for a snapshot plan, and whether the plan publishes images
// (Publish) and keeps the built files (PublishFiles).
type PlanInput struct {
	File, Branch, SourceSHA, ImageRepository string
	Publish, PublishFiles                    bool
}

// DefaultConfigFile is optional for projects without declared containers.
// A caller-selected custom path must exist, so typos cannot silently disable
// publication.
const DefaultConfigFile = ".forge-ci/artifacts.yml"

// CheckSource pins the worktree commit without trusting a detached HEAD's name.
// Logical branch is explicit and is never replaced by the event/default branch.
func CheckSource(ctx context.Context, ops appversion.GenerateSnapshotOps, sha string) error {
	head, err := ops.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	if head != sha {
		return fmt.Errorf("snapshot HEAD %s %w %s", head, errHeadDiffers, sha)
	}

	return nil
}

// Plan derives the frozen snapshot plan from the pinned source and configuration.
func Plan(ctx context.Context, ops appversion.GenerateSnapshotOps, in PlanInput) (snapshotimage.Plan, error) {
	if err := CheckSource(ctx, ops, in.SourceSHA); err != nil {
		return snapshotimage.Plan{}, err
	}

	cfg, err := loadConfig(in.File)
	if err != nil {
		return snapshotimage.Plan{}, err
	}

	// The snapshot reads the release declaration and takes what it builds,
	// its artifacts and containers. What only a release does, the sign and
	// git-signing blocks and a container's enable-slsa, the planner never
	// reads: every snapshot image is unsigned and carries no provenance.
	typed := pipeline.NewConfigPlan(cfg)

	var tag bytes.Buffer
	if err := appversion.GenerateSnapshotVersion(ctx, ops, &tag, appversion.GenerateSnapshotVersionInput{RefName: in.Branch, ContainerTag: true}); err != nil {
		return snapshotimage.Plan{}, err
	}

	// Every producer, an image's or a file output's, builds with the frozen
	// source timestamp.
	epoch := ""
	if len(typed.Artifacts.All) > 0 && (in.PublishFiles || in.Publish && len(typed.Containers.All) > 0) {
		epoch, err = ops.Run(ctx, "show", "-s", "--format=%ct", "HEAD")
		if err != nil {
			return snapshotimage.Plan{}, err
		}
	}

	return snapshotimage.New(typed, snapshotimage.Input{SourceSHA: in.SourceSHA, SourceBranch: in.Branch, Tag: strings.TrimSpace(tag.String()), ImageRepository: in.ImageRepository, SourceDateEpoch: epoch, Publish: in.Publish, PublishFiles: in.PublishFiles})
}

// loadConfig reads, validates and derives the artifact configuration. Only the
// default path may be absent; it then yields an empty configuration.
func loadConfig(file string) (*config.Config, error) {
	cfg := &config.Config{}

	if file == "" {
		file = DefaultConfigFile
	}

	body, err := cliio.ReadRegularFile(file)
	if err != nil && (!errors.Is(err, fs.ErrNotExist) || filepath.Clean(file) != DefaultConfigFile) {
		return nil, err
	}

	if err == nil {
		cfg, err = config.Parse(body)
		if err != nil {
			return nil, err
		}
	}

	if len(cfg.Artifacts)+len(cfg.Containers) > 0 {
		if err = config.Validate(cfg); err != nil {
			return nil, err
		}
	}

	if err = config.Derive(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// ReadJSON rejects unknown fields, trailing documents and non-regular inputs.
func ReadJSON(path string, value any) error {
	body, err := readJSONFile(path)
	if err != nil {
		return err
	}

	return decodeJSON(body, value)
}

func decodeJSON(body []byte, value any) error {
	if err := uniqueJSONKeys(body); err != nil {
		return err
	}

	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()

	if err := d.Decode(value); err != nil {
		return err
	}

	if err := d.Decode(new(any)); err != io.EOF {
		return errJSONSingleDocument
	}

	return nil
}

// ReadPlan reads a frozen snapshot plan strictly and validates it.
func ReadPlan(path string) (snapshotimage.Plan, error) {
	var plan snapshotimage.Plan
	if err := ReadJSON(path, &plan); err != nil {
		return plan, err
	}

	return plan, plan.Validate()
}

// WriteJSON writes value as one JSON document to a regular, non-symlink file.
func WriteJSON(path string, value any) error {
	if path == "" || path == "-" {
		return errOutputNotRegular
	}

	parent, err := pathsafe.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}

	defer func() { _ = parent.Close() }()

	if info, err := parent.Lstat(filepath.Base(path)); err == nil && !info.Mode().IsRegular() {
		return errJSONOutputNotRegular
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	body, err := json.Marshal(value)
	if err != nil {
		return err
	}

	return cliio.WriteFile(path, append(body, '\n'), 0o600)
}

// EmitPlan publishes the snapshot plan and its derived matrices as CI outputs.
func EmitPlan(ctx context.Context, sink ci.OutputSink, plan snapshotimage.Plan) error {
	transfers, err := plan.ArtifactTransfers()
	if err != nil {
		return err
	}

	if err := sink.Set(ctx, "source-sha", plan.SourceSHA); err != nil {
		return err
	}

	hasARM64 := false
	for _, build := range plan.Builds {
		hasARM64 = hasARM64 || build.Arch == "arm64"
	}

	for _, flag := range []struct {
		key   string
		value bool
	}{{"enabled", plan.Enabled}, {"has-artifacts", len(plan.Artifacts) > 0}, {"has-arm64", hasARM64}, {"has-files", len(plan.Files) > 0}} {
		if err := sink.SetBool(ctx, flag.key, flag.value); err != nil {
			return err
		}
	}

	for _, item := range []struct {
		name  string
		value any
	}{{"plan-json", plan}, {"builds", plan.Builds}, {"images", plan.Images}, {"artifacts", plan.Artifacts}, {"artifact-transfer-plan-json", transfers}, {"files", plan.FileOutputs()}} {
		body, err := json.Marshal(item.value)
		if err != nil {
			return err
		}

		if err = sink.Set(ctx, item.name, string(body)); err != nil {
			return err
		}
	}

	return nil
}

// BuildInput selects one native build and its workspace-relative artifact root.
type BuildInput struct {
	Name, Arch, HostArch, MetadataDir, AuthFile, ServerURL, Repository string
	DependenciesFile                                                   string
}

// Build refuses a foreign architecture and absent artifact inputs before tools
// can mutate the metadata directory. Layouts retain the established native format.
func Build(ctx context.Context, ops appversion.GenerateSnapshotOps, tool appcontainer.SignerImageTool, inspector appcontainer.LayoutInspector, out io.Writer, annot output.Annotator, plan snapshotimage.Plan, in BuildInput) error {
	if err := plan.Validate(); err != nil {
		return err
	}

	if err := CheckSource(ctx, ops, plan.SourceSHA); err != nil {
		return err
	}

	if in.Arch != in.HostArch {
		return fmt.Errorf("snapshot architecture %s %w (host %s)", in.Arch, errNativeRunner, in.HostArch)
	}

	if !pathsafe.Relative(in.MetadataDir) {
		return errMetadataDirRelative
	}

	for _, build := range plan.Builds {
		if build.Name != in.Name || build.Arch != in.Arch {
			continue
		}

		inventoryFile := cmp.Or(in.DependenciesFile, snapshotimage.DefaultDependenciesFile)

		inventory, err := validateDependencies(plan, inventoryFile)
		if err != nil {
			return err
		}

		if err := layNPMPackage(build, inventory, inventoryFile); err != nil {
			return err
		}

		args := make([]string, 0, len(build.BuildArgs))
		for k, v := range build.BuildArgs {
			args = append(args, k+"="+v)
		}

		sort.Strings(args)
		_, err = appcontainer.StageSignerImageArch(ctx, tool, inspector, out, appcontainer.SignerImageBuildArchInput{
			AuthFile: in.AuthFile, Arch: in.Arch, HostArch: in.HostArch,
			SourceSHA: plan.SourceSHA, ServerURL: in.ServerURL, Repository: in.Repository,
			ImageRepository: build.ImageRepository, TagName: build.Tag, Name: in.Name, Title: in.Name,
			Containerfile: build.ContainerFile, Context: build.Context, Target: build.Target, BuildArgs: args,
			MetadataDir: filepath.Join(in.MetadataDir, in.Name+"-arch-"+in.Arch),
			Labels:      []string{"org.opencontainers.image.version=" + build.Tag, "org.opencontainers.image.ref.name=" + plan.SourceBranch},
		})

		return err
	}

	return fmt.Errorf("%w %s/%s", errUnplannedBuild, in.Name, in.Arch)
}

// PublishInput selects one planned image and its metadata, auth and evidence paths.
type PublishInput struct {
	Name, MetadataDir, AuthFile, ServerURL, Repository, EvidenceDir string
}

// Publish verifies the entire native platform set, then records configured
// evidence from those exact bytes before PublishAndAssemble uploads anything.
func Publish(ctx context.Context, registry appcontainer.StagedSignerRegistry, scanner appsecurity.TrivyOps, syft appcontainer.ImageEvidenceSyft, sink ci.OutputSink, summary ci.SummarySink, out io.Writer, annot output.Annotator, plan snapshotimage.Plan, in PublishInput) (snapshotimage.Receipt, error) {
	im, err := plan.Image(in.Name)
	if err != nil {
		return snapshotimage.Receipt{}, err
	}

	if !pathsafe.Relative(in.EvidenceDir) {
		return snapshotimage.Receipt{}, errEvidenceDirRelative
	}

	archs, err := readPlatformLayouts(im, in.MetadataDir)
	if err != nil {
		return snapshotimage.Receipt{}, err
	}

	// Every published snapshot image is scanned; a frozen plan cannot opt out.
	if scanner == nil {
		return snapshotimage.Receipt{}, errScanRequiresTrivy
	}

	if im.SBOM && syft == nil {
		return snapshotimage.Receipt{}, errSBOMRequiresSyft
	}

	evidence := &appcontainer.StagedLayoutEvidence{CheckLayout: func(ctx context.Context, layout, platform string) error {
		dir := filepath.Join(in.EvidenceDir, im.Name, strings.ReplaceAll(platform, "/", "-"))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}

		if err := appsecurity.ScanContainer(ctx, scanner, out, out, annot, appsecurity.ScanContainerInput{ImageRef: im.ImageRepository + ":" + im.Tag, OCILayout: layout, Platform: platform, Severity: im.Severity, JSONFile: filepath.Join(dir, "trivy.json"), SARIFFile: filepath.Join(dir, "trivy.sarif"), GitLabReportFile: filepath.Join(dir, "gitlab.json")}); err != nil {
			return err
		}

		if im.SBOM {
			return inventory(ctx, syft, layout, filepath.Join(dir, "sbom.cyclonedx.json"), out)
		}

		return nil
	}}

	meta, err := appcontainer.PublishAndAssembleSignerImage(ctx, registry, sink, summary, out, appcontainer.SignerImageAssembleInput{
		AuthFile: in.AuthFile, SourceSHA: plan.SourceSHA, ServerURL: in.ServerURL, Repository: in.Repository,
		ImageRepository: im.ImageRepository, TagName: im.Tag, Name: im.Name, Archs: archs,
		LayoutsDir: in.MetadataDir, MetadataDir: filepath.Join(in.EvidenceDir, im.Name+"-dist"),
		RequiredLabels: []string{"org.opencontainers.image.version=" + im.Tag, "org.opencontainers.image.ref.name=" + plan.SourceBranch},
		Evidence:       evidence,
	})
	if err != nil {
		return snapshotimage.Receipt{}, err
	}

	r := snapshotimage.Receipt{Version: snapshotimage.Version, SourceSHA: plan.SourceSHA, SourceBranch: plan.SourceBranch, Name: im.Name, ImageRepository: im.ImageRepository, Tag: im.Tag, Digest: meta.Digest, Ref: meta.Ref, Platforms: im.Platforms}

	return r, nil
}

// readPlatformLayouts strictly reads every native platform's staged metadata
// and layout record, returning the architectures in plan order.
func readPlatformLayouts(im snapshotimage.Image, metadataDir string) ([]string, error) {
	archs := make([]string, 0, len(im.Platforms))
	for _, platform := range im.Platforms {
		arch := strings.TrimPrefix(platform, "linux/")
		archs = append(archs, arch)
		directory := filepath.Join(metadataDir, im.Name+"-arch-"+arch)

		var metadata appcontainer.SignerImageArchMetadata
		if err := ReadJSON(filepath.Join(directory, im.Name+"-"+arch+".json"), &metadata); err != nil {
			return nil, err
		}

		var record appcontainer.ImageLayoutRecord
		if err := ReadJSON(filepath.Join(directory, "layout", "forge-ci-image.json"), &record); err != nil {
			return nil, err
		}
	}

	return archs, nil
}

func inventory(ctx context.Context, syft appcontainer.ImageEvidenceSyft, layout, destination string, out io.Writer) error {
	stage, err := pathsafe.NewArtifactStaging(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer func() { _ = stage.Close() }()

	path := filepath.Join(stage.Root().Name(), filepath.Base(destination))
	if err = syft.Generate(ctx, "oci-dir:"+layout, map[string]string{"cyclonedx-json": path}, out); err != nil {
		return err
	}

	var doc struct {
		Format      string `json:"bomFormat"`
		SpecVersion string `json:"specVersion"`
		Version     int    `json:"version"`
	}

	body, err := cliio.ReadRegularFile(path)
	if err != nil {
		return err
	}

	if json.Unmarshal(body, &doc) != nil || doc.Format != "CycloneDX" || doc.SpecVersion == "" || doc.Version < 1 {
		return errInvalidCycloneDX
	}

	return stage.Install()
}

// Collect orders the per-image publication receipts against the plan and emits
// them as a JSON file and CI outputs.
func Collect(ctx context.Context, sink ci.OutputSink, plan snapshotimage.Plan, dir, destination string) error {
	if err := plan.Validate(); err != nil {
		return err
	}

	entries, err := os.ReadDir(dir)
	if err != nil && (plan.Enabled || !errors.Is(err, fs.ErrNotExist)) {
		return err
	}

	receipts := []snapshotimage.Receipt{}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("%w %s", errUnexpectedReceiptEntry, entry.Name())
		}

		var r snapshotimage.Receipt
		if err := ReadJSON(filepath.Join(dir, entry.Name()), &r); err != nil {
			return err
		}

		receipts = append(receipts, r)
	}

	ordered, err := snapshotimage.Collect(plan, receipts)
	if err != nil {
		return err
	}

	if err = WriteJSON(destination, ordered); err != nil {
		return err
	}

	body, err := json.Marshal(ordered)
	if err != nil {
		return err
	}

	if err = sink.Set(ctx, "container-images-json", string(body)); err != nil {
		return err
	}

	refs := make([]string, 0, len(ordered))
	for _, r := range ordered {
		refs = append(refs, r.Ref)
	}
	// Compact JSON is portable across GitLab dotenv and Actions outputs; a
	// multiline output is unsupported by the GitLab adapter even for one image.
	encodedRefs, err := json.Marshal(refs)
	if err != nil {
		return err
	}

	return sink.Set(ctx, "digestrefs", string(encodedRefs))
}
