// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package snapshotimage implements the shared snapshot-image boundary.
package snapshotimage

import (
	"bytes"
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

type PlanInput struct {
	File, Branch, SourceSHA, ImageRepository string
	Publish                                  bool
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
		return fmt.Errorf("snapshot HEAD %s differs from source-sha %s", head, sha)
	}
	return nil
}

func Plan(ctx context.Context, ops appversion.GenerateSnapshotOps, in PlanInput) (snapshotimage.Plan, error) {
	if err := CheckSource(ctx, ops, in.SourceSHA); err != nil {
		return snapshotimage.Plan{}, err
	}
	cfg := &config.Config{}
	file := in.File
	if file == "" {
		file = DefaultConfigFile
	}
	body, err := cliio.ReadRegularFile(file)
	if err != nil && (!errors.Is(err, fs.ErrNotExist) || filepath.Clean(file) != DefaultConfigFile) {
		return snapshotimage.Plan{}, err
	}
	if err == nil {
		cfg, err = config.Parse(body)
		if err != nil {
			return snapshotimage.Plan{}, err
		}
	}
	if len(cfg.Artifacts)+len(cfg.Containers) > 0 {
		if err = config.Validate(cfg); err != nil {
			return snapshotimage.Plan{}, err
		}
	}
	if err = config.Derive(cfg); err != nil {
		return snapshotimage.Plan{}, err
	}
	typed := pipeline.NewConfigPlan(cfg)
	for i, c := range cfg.Containers {
		// Snapshot evidence is deliberately independent of production signing.
		if in.Publish && c.EnableSLSA != nil && *c.EnableSLSA {
			return snapshotimage.Plan{}, fmt.Errorf("container %s explicitly requests unsupported snapshot OCI provenance", c.Name)
		}
		typed.Containers.All[i].EnableSLSA = false
	}
	if in.Publish && len(cfg.Containers) > 0 && typed.Sign.SignsContainers {
		return snapshotimage.Plan{}, fmt.Errorf("configured OCI signing is unavailable for snapshots; signing must be NONE")
	}
	var tag bytes.Buffer
	if err := appversion.GenerateSnapshotVersion(ctx, ops, &tag, appversion.GenerateSnapshotVersionInput{RefName: in.Branch, ContainerTag: true}); err != nil {
		return snapshotimage.Plan{}, err
	}
	epoch := ""
	if in.Publish && len(typed.Artifacts.All) > 0 && len(typed.Containers.All) > 0 {
		epoch, err = ops.Run(ctx, "show", "-s", "--format=%ct", "HEAD")
		if err != nil {
			return snapshotimage.Plan{}, err
		}
	}
	return snapshotimage.New(typed, snapshotimage.Input{SourceSHA: in.SourceSHA, SourceBranch: in.Branch, Tag: strings.TrimSpace(tag.String()), ImageRepository: in.ImageRepository, SourceDateEpoch: epoch, Publish: in.Publish})
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
		return fmt.Errorf("expected exactly one JSON document")
	}
	return nil
}

func ReadPlan(path string) (snapshotimage.Plan, error) {
	var p snapshotimage.Plan
	if err := ReadJSON(path, &p); err != nil {
		return p, err
	}
	return p, p.Validate()
}

func WriteJSON(path string, value any) error {
	if path == "" || path == "-" {
		return fmt.Errorf("output must name a regular file")
	}
	parent, err := pathsafe.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if info, err := parent.Lstat(filepath.Base(path)); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("snapshot JSON output must be a regular file")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return cliio.WriteFile(path, append(body, '\n'), 0o600)
}

func EmitPlan(ctx context.Context, sink ci.OutputSink, p snapshotimage.Plan) error {
	transfers, err := p.ArtifactTransfers()
	if err != nil {
		return err
	}
	if err := sink.Set(ctx, "source-sha", p.SourceSHA); err != nil {
		return err
	}
	hasARM64 := false
	for _, build := range p.Builds {
		hasARM64 = hasARM64 || build.Arch == "arm64"
	}
	for _, flag := range []struct {
		key   string
		value bool
	}{{"enabled", p.Enabled}, {"has-artifacts", len(p.Artifacts) > 0}, {"has-arm64", hasARM64}} {
		if err := sink.SetBool(ctx, flag.key, flag.value); err != nil {
			return err
		}
	}
	for _, item := range []struct {
		name  string
		value any
	}{{"plan-json", p}, {"builds", p.Builds}, {"images", p.Images}, {"artifacts", p.Artifacts}, {"artifact-transfer-plan-json", transfers}} {
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
func Build(ctx context.Context, ops appversion.GenerateSnapshotOps, tool appcontainer.SignerImageTool, inspector appcontainer.LayoutInspector, out io.Writer, annot output.Annotator, p snapshotimage.Plan, in BuildInput) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := CheckSource(ctx, ops, p.SourceSHA); err != nil {
		return err
	}
	if in.Arch != in.HostArch {
		return fmt.Errorf("snapshot architecture %s requires its native runner (host %s)", in.Arch, in.HostArch)
	}
	if !pathsafe.Relative(in.MetadataDir) {
		return fmt.Errorf("snapshot build metadata-dir must be workspace-relative")
	}
	for _, b := range p.Builds {
		if b.Name != in.Name || b.Arch != in.Arch {
			continue
		}
		if err := validateDependencies(p, in.DependenciesFile); err != nil {
			return err
		}
		args := make([]string, 0, len(b.BuildArgs))
		for k, v := range b.BuildArgs {
			args = append(args, k+"="+v)
		}
		sort.Strings(args)
		_, err := appcontainer.StageSignerImageArch(ctx, tool, inspector, out, appcontainer.SignerImageBuildArchInput{
			AuthFile: in.AuthFile, Arch: in.Arch, HostArch: in.HostArch,
			SourceSHA: p.SourceSHA, ServerURL: in.ServerURL, Repository: in.Repository,
			ImageRepository: b.ImageRepository, TagName: b.Tag, Name: in.Name, Title: in.Name,
			Containerfile: b.ContainerFile, Context: b.Context, Target: b.Target, BuildArgs: args,
			MetadataDir: filepath.Join(in.MetadataDir, in.Name+"-arch-"+in.Arch),
			Labels:      []string{"org.opencontainers.image.version=" + b.Tag, "org.opencontainers.image.ref.name=" + p.SourceBranch},
		})
		return err
	}
	return fmt.Errorf("unplanned snapshot build %s/%s", in.Name, in.Arch)
}

type PublishInput struct {
	Name, MetadataDir, AuthFile, ServerURL, Repository, EvidenceDir string
}

// Publish verifies the entire native platform set, then records configured
// evidence from those exact bytes before PublishAndAssemble uploads anything.
func Publish(ctx context.Context, registry appcontainer.StagedSignerRegistry, scanner appsecurity.TrivyOps, syft appcontainer.ImageEvidenceSyft, sink ci.OutputSink, summary ci.SummarySink, out io.Writer, annot output.Annotator, p snapshotimage.Plan, in PublishInput) (snapshotimage.Receipt, error) {
	im, err := p.Image(in.Name)
	if err != nil {
		return snapshotimage.Receipt{}, err
	}
	if !pathsafe.Relative(in.EvidenceDir) {
		return snapshotimage.Receipt{}, fmt.Errorf("snapshot evidence-dir must be workspace-relative")
	}
	archs := make([]string, 0, len(im.Platforms))
	for _, platform := range im.Platforms {
		arch := strings.TrimPrefix(platform, "linux/")
		archs = append(archs, arch)
		directory := filepath.Join(in.MetadataDir, im.Name+"-arch-"+arch)
		var metadata appcontainer.SignerImageArchMetadata
		if err := ReadJSON(filepath.Join(directory, im.Name+"-"+arch+".json"), &metadata); err != nil {
			return snapshotimage.Receipt{}, err
		}
		var record appcontainer.ImageLayoutRecord
		if err := ReadJSON(filepath.Join(directory, "layout", "forge-ci-image.json"), &record); err != nil {
			return snapshotimage.Receipt{}, err
		}
	}
	if im.Scan && scanner == nil {
		return snapshotimage.Receipt{}, fmt.Errorf("snapshot scan policy requires Trivy")
	}
	if im.SBOM && syft == nil {
		return snapshotimage.Receipt{}, fmt.Errorf("snapshot SBOM policy requires Syft")
	}
	evidence := &appcontainer.StagedLayoutEvidence{CheckLayout: func(ctx context.Context, layout, platform string) error {
		if !im.Scan && !im.SBOM {
			return nil
		}
		dir := filepath.Join(in.EvidenceDir, im.Name, strings.ReplaceAll(platform, "/", "-"))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if im.Scan {
			if err := appsecurity.ScanContainer(ctx, scanner, out, out, annot, appsecurity.ScanContainerInput{ImageRef: im.ImageRepository + ":" + im.Tag, OCILayout: layout, Severity: im.Severity, JSONFile: filepath.Join(dir, "trivy.json"), SARIFFile: filepath.Join(dir, "trivy.sarif"), GitLabReportFile: filepath.Join(dir, "gitlab.json")}); err != nil {
				return err
			}
		}
		if im.SBOM {
			return inventory(ctx, syft, layout, filepath.Join(dir, "sbom.cyclonedx.json"), out)
		}
		return nil
	}}
	meta, err := appcontainer.PublishAndAssembleSignerImage(ctx, registry, sink, summary, out, appcontainer.SignerImageAssembleInput{
		AuthFile: in.AuthFile, SourceSHA: p.SourceSHA, ServerURL: in.ServerURL, Repository: in.Repository,
		ImageRepository: im.ImageRepository, TagName: im.Tag, Name: im.Name, Archs: archs,
		LayoutsDir: in.MetadataDir, MetadataDir: filepath.Join(in.EvidenceDir, im.Name+"-dist"),
		RequiredLabels: []string{"org.opencontainers.image.version=" + im.Tag, "org.opencontainers.image.ref.name=" + p.SourceBranch},
		Evidence:       evidence,
	})
	if err != nil {
		return snapshotimage.Receipt{}, err
	}
	r := snapshotimage.Receipt{Version: snapshotimage.Version, SourceSHA: p.SourceSHA, SourceBranch: p.SourceBranch, Name: im.Name, ImageRepository: im.ImageRepository, Tag: im.Tag, Digest: meta.Digest, Ref: meta.Ref, Platforms: im.Platforms}
	return r, nil
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
		return fmt.Errorf("invalid snapshot CycloneDX inventory")
	}
	return stage.Install()
}

func Collect(ctx context.Context, sink ci.OutputSink, p snapshotimage.Plan, dir, destination string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && (p.Enabled || !errors.Is(err, fs.ErrNotExist)) {
		return err
	}
	receipts := []snapshotimage.Receipt{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("unexpected snapshot receipt entry %s", entry.Name())
		}
		var r snapshotimage.Receipt
		if err := ReadJSON(filepath.Join(dir, entry.Name()), &r); err != nil {
			return err
		}
		receipts = append(receipts, r)
	}
	ordered, err := snapshotimage.Collect(p, receipts)
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
