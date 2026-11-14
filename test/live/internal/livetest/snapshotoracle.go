// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
)

// SnapshotRun is what a snapshot run left for the oracle to judge: the
// declaration it planned at the selected source, that source's branch and
// head, the frozen plan the run exported (snapshot-plan.json) and its
// collected receipts (container-images.json).
type SnapshotRun struct {
	Declaration    string
	Branch, Head   string
	Plan, Receipts []byte
}

// SnapshotRegistry reads the one image repository a snapshot run published
// to, on whichever forge's registry it is.
type SnapshotRegistry struct {
	repository name.Repository
	options    []remote.Option
}

// LabSnapshotRegistry is the run's image repository repo in a lab forge's
// registry, read with the lab's credential over its frozen-CA transport.
func LabSnapshotRegistry(tb TB, target Target, repo string) SnapshotRegistry {
	tb.Helper()
	requireAccepted(tb, target)

	if !inRunNamespace(target, repo) {
		tb.Fatalf("livetest: snapshot verification requires a repository of the run")
	}

	host, err := RegistryHost(target)
	if err != nil {
		tb.Fatalf("livetest: %v", err)
	}

	options, err := registryRemoteOptions(target)
	if err != nil {
		tb.Fatalf("livetest: constrain registry transport: %v", err)
	}

	return newSnapshotRegistry(tb, host+"/"+strings.ToLower(RepoSlug(target, repo)), options)
}

// SnapshotRegistry is repo's container package on GHCR, the snapshot
// orchestrator's default image repository: a public consumer's package is
// read anonymously, since anyone must be able to pull it, and a private
// one's with the context's actor and management/Packages token, the
// credential CheckImage hands cosign. The host's Docker configuration is
// consulted for neither.
func (trial *GitHubTrial) SnapshotRegistry(repo string, private bool) SnapshotRegistry {
	trial.tb.Helper()

	auth := authn.Anonymous
	if private {
		auth = &authn.Basic{Username: trial.actor, Password: trial.token}
	}

	return newSnapshotRegistry(trial.tb, "ghcr.io/"+strings.ToLower(trial.org+"/"+repo), []remote.Option{remote.WithAuth(auth)})
}

func newSnapshotRegistry(tb TB, image string, options []remote.Option) SnapshotRegistry {
	tb.Helper()

	repository, err := name.NewRepository(image)
	if err != nil {
		tb.Fatalf("livetest: snapshot image repository %s: %v", image, err)
	}

	return SnapshotRegistry{repository: repository, options: options}
}

// Image is the image repository the planner is told to plan into.
func (registry SnapshotRegistry) Image() string {
	return registry.repository.Name()
}

func (registry SnapshotRegistry) remote(ctx context.Context) []remote.Option {
	return append(append([]remote.Option{}, registry.options...), remote.WithContext(ctx))
}

// RequireSnapshotImages is the one snapshot oracle every forge's trial
// judges a snapshot run by. The frozen plan must be the plan the snapshot
// planner makes of the run's declaration for the selected source, into the
// registry's image repository; the receipts must be one per planned image,
// bound to it as the product's own collection requires
// (snapshotimage.Collect); each receipt's tag must resolve to its digest,
// and each planned platform of that digest must be a linux image of its
// architecture labelled with the selected head and branch; the digest's
// manifest must be an index of those platforms and no other child. The repository
// may hold no tag but each image's tag and its per-architecture tags. It
// returns the receipts, in plan order, and the expected plan.
func RequireSnapshotImages(ctx context.Context, tb TB, run SnapshotRun, registry SnapshotRegistry) ([]snapshotimage.Receipt, snapshotimage.Plan) {
	tb.Helper()

	var frozen snapshotimage.Plan
	if err := json.Unmarshal(run.Plan, &frozen); err != nil {
		tb.Fatalf("missing frozen snapshot plan: %v", err)
	}

	expected, err := planSnapshotDeclaration(run.Declaration, snapshotimage.Input{
		SourceSHA: run.Head, SourceBranch: run.Branch, Tag: frozen.Tag, ImageRepository: registry.Image(),
		SourceDateEpoch: frozen.SourceDateEpoch, Publish: true,
	})
	if err != nil {
		tb.Fatalf("plan the run's snapshot declaration: %v", err)
	}

	if err := frozen.Validate(); err != nil || frozen.SourceSHA != run.Head || frozen.SourceBranch != run.Branch || !reflect.DeepEqual(frozen, expected) {
		tb.Fatalf("wrong frozen snapshot plan: %+v, want %+v: %v", frozen, expected, err)
	}

	var receipts []snapshotimage.Receipt
	if err := json.Unmarshal(run.Receipts, &receipts); err != nil {
		tb.Fatalf("missing snapshot receipts: %v", err)
	}

	receipts, err = snapshotimage.Collect(frozen, receipts)
	if err != nil {
		tb.Fatalf("the receipts do not bind the selected source and every planned image: %v", err)
	}

	tags := map[string]bool{}

	for _, receipt := range receipts {
		registry.requireReceipt(ctx, tb, run, receipt)

		tags[receipt.Tag] = true
		for _, platform := range receipt.Platforms {
			tags[receipt.Tag+"-"+platform[strings.LastIndex(platform, "/")+1:]] = true
		}
	}

	listed, err := remote.List(registry.repository, registry.remote(ctx)...)
	if err != nil {
		tb.Fatalf("livetest: list the tags of %s: %v", registry.Image(), err)
	}

	for _, tag := range listed {
		if !tags[tag] {
			tb.Errorf("unexpected registry alias %s:%s", registry.Image(), tag)
		}
	}

	return receipts, expected
}

// RequireSnapshotFilesPlan is the plan half of the oracle for a snapshot of
// file outputs alone: the frozen plan must be the plan the snapshot planner
// makes of the run's declaration for the selected source when asked for its
// files, into no image repository, and it must plan a file output and no
// image. It returns the expected plan.
func RequireSnapshotFilesPlan(tb TB, run SnapshotRun) snapshotimage.Plan {
	tb.Helper()

	var frozen snapshotimage.Plan
	if err := json.Unmarshal(run.Plan, &frozen); err != nil {
		tb.Fatalf("missing frozen snapshot plan: %v", err)
	}

	expected, err := planSnapshotDeclaration(run.Declaration, snapshotimage.Input{
		SourceSHA: run.Head, SourceBranch: run.Branch, Tag: frozen.Tag, SourceDateEpoch: frozen.SourceDateEpoch, Publish: true,
	})
	if err != nil {
		tb.Fatalf("plan the run's snapshot declaration: %v", err)
	}

	if err := frozen.Validate(); err != nil || frozen.SourceSHA != run.Head || frozen.SourceBranch != run.Branch || !reflect.DeepEqual(frozen, expected) {
		tb.Fatalf("wrong frozen snapshot plan: %+v, want %+v: %v", frozen, expected, err)
	}

	if len(expected.Files) == 0 || len(expected.Images) != 0 {
		tb.Fatalf("the snapshot plans %d file outputs and %d images, want file outputs alone", len(expected.Files), len(expected.Images))
	}

	return expected
}

// SnapshotFilesFindings judges the bundles a snapshot of file outputs kept,
// the tree of each by file output name, against the expected plan. A planned
// output keeps one bundle, whose receipt (snapshot-files.json) names the
// output, its project type, the plan's source commit, branch and snapshot
// version, and no signing; every file the receipt lists is in the bundle with
// the listed size and sha256, the receipt lists at least one, for an npm
// output its one packed tarball alone, and the bundle holds nothing else. A
// bundle of an output the plan does not have is a finding too. It returns the
// findings, each naming what differed, none for a run that kept what it
// planned.
func SnapshotFilesFindings(plan snapshotimage.Plan, bundles map[string]map[string][]byte) []string {
	var findings []string

	planned := map[string]bool{}

	for _, file := range plan.Files {
		planned[file.Name] = true

		tree, kept := bundles[file.Name]
		if !kept {
			findings = append(findings, fmt.Sprintf("file output %s kept no bundle", file.Name))

			continue
		}

		findings = append(findings, snapshotBundleFindings(plan, file, tree)...)
	}

	for _, name := range slices.Sorted(maps.Keys(bundles)) {
		if !planned[name] {
			findings = append(findings, "bundle of unplanned file output "+name)
		}
	}

	return findings
}

// snapshotBundleFindings judges the one bundle a planned file output kept:
// its receipt against the plan, then the files the receipt lists against
// the bundle's tree (snapshotListedFindings).
func snapshotBundleFindings(plan snapshotimage.Plan, file pipeline.PlannedArtifact, tree map[string][]byte) []string {
	var receipt snapshotimage.FilesReceipt
	if err := json.Unmarshal(tree[snapshotimage.FilesManifest], &receipt); err != nil {
		return []string{fmt.Sprintf("file output %s kept no readable %s: %v", file.Name, snapshotimage.FilesManifest, err)}
	}

	var findings []string

	listed := receipt.Files
	receipt.Files = nil

	want := snapshotimage.FilesReceipt{
		Version: snapshotimage.Version, SourceSHA: plan.SourceSHA, SourceBranch: plan.SourceBranch,
		SnapshotVersion: plan.ArtifactVersion(), Name: file.Name, ProjectType: file.ProjectType, Signing: snapshotimage.FilesSigning,
	}
	if !reflect.DeepEqual(receipt, want) {
		findings = append(findings, fmt.Sprintf("the receipt of file output %s reads %+v, want %+v", file.Name, receipt, want))
	}

	switch {
	case len(listed) == 0:
		findings = append(findings, fmt.Sprintf("the receipt of file output %s lists no file", file.Name))
	case file.ProjectType == projecttype.NPM && (len(listed) != 1 || !strings.HasSuffix(listed[0].Path, ".tgz")):
		findings = append(findings, fmt.Sprintf("the receipt of npm file output %s lists %d files, the first %s; want its one packed tarball", file.Name, len(listed), listed[0].Path))
	}

	return append(findings, snapshotListedFindings(file.Name, listed, tree)...)
}

// snapshotListedFindings holds the tree of the bundle of the file output
// named name to the files its receipt lists: each listed file is there with
// its listed size and sha256, and the tree holds nothing but them and the
// receipt.
func snapshotListedFindings(name string, listed []snapshotimage.DependencyFile, tree map[string][]byte) []string {
	var findings []string

	admitted := map[string]bool{snapshotimage.FilesManifest: true}

	for _, entry := range listed {
		admitted[entry.Path] = true

		content, present := tree[entry.Path]
		if !present {
			findings = append(findings, fmt.Sprintf("file output %s lists %s, which its bundle does not hold", name, entry.Path))

			continue
		}

		if digest := fmt.Sprintf("%x", sha256.Sum256(content)); digest != entry.SHA256 || int64(len(content)) != entry.Size {
			findings = append(findings, fmt.Sprintf("file output %s holds %s of %d bytes and sha256 %s, and its receipt lists %d bytes and sha256 %s",
				name, entry.Path, len(content), digest, entry.Size, entry.SHA256))
		}
	}

	for _, path := range slices.Sorted(maps.Keys(tree)) {
		if !admitted[path] {
			findings = append(findings, fmt.Sprintf("file output %s holds %s, which its receipt does not list", name, path))
		}
	}

	return findings
}

// requireReceipt checks that the receipt's tag serves its digest and that
// each planned platform of it is the selected source's image.
func (registry SnapshotRegistry) requireReceipt(ctx context.Context, tb TB, run SnapshotRun, receipt snapshotimage.Receipt) {
	tb.Helper()

	if receipt.ImageRepository != registry.Image() {
		tb.Fatalf("the receipt of %s names %s, not the run's image repository %s", receipt.Name, receipt.ImageRepository, registry.Image())
	}

	tagged, err := remote.Head(registry.repository.Tag(receipt.Tag), registry.remote(ctx)...)
	if err != nil || tagged.Digest.String() != receipt.Digest {
		tb.Fatalf("snapshot tag %s does not resolve to receipt digest %s: %v", receipt.Tag, receipt.Digest, err)
	}

	// Read by digest: the transport refuses bytes that do not hash to it.
	published, err := remote.Get(registry.repository.Digest(receipt.Digest), registry.remote(ctx)...)
	if err != nil {
		tb.Fatalf("read the published manifest of %s: %v", receipt.Ref, err)
	}

	if err := snapshotManifestPlatforms(published.MediaType, published.Manifest, receipt.Platforms); err != nil {
		tb.Fatalf("published image %s (%s) %v", receipt.Name, receipt.Ref, err)
	}

	for _, platform := range receipt.Platforms {
		system, architecture, _ := strings.Cut(platform, "/")

		image, err := remote.Image(registry.repository.Digest(receipt.Digest),
			append(registry.remote(ctx), remote.WithPlatform(v1.Platform{OS: system, Architecture: architecture}))...)
		if err != nil {
			tb.Fatalf("read the %s image of %s: %v", platform, receipt.Ref, err)
		}

		config, err := image.ConfigFile()
		if err != nil {
			tb.Fatalf("read the %s image config of %s: %v", platform, receipt.Ref, err)
		}

		if config.OS != system || config.Architecture != architecture ||
			config.Config.Labels["org.opencontainers.image.revision"] != run.Head ||
			config.Config.Labels["org.opencontainers.image.ref.name"] != run.Branch {
			tb.Fatalf("published image %s (%s) does not match the selected platform, source and branch", receipt.Name, platform)
		}
	}
}

// snapshotManifestPlatforms holds a published manifest to its planned
// platforms. A snapshot publishes an index, of one platform too, with one
// child per planned platform and no other, so no unplanned architecture and
// no attestation manifest rides along with it.
func snapshotManifestPlatforms(mediaType types.MediaType, raw []byte, platforms []string) error {
	if !mediaType.IsIndex() {
		return fmt.Errorf("has the media type %q, not an index: %w", mediaType, errs.ErrValidation)
	}

	index, err := v1.ParseIndexManifest(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("is no readable index: %w", err)
	}

	children := make([]string, 0, len(index.Manifests))

	for _, child := range index.Manifests {
		platform := "none"
		if child.Platform != nil {
			platform = child.Platform.OS + "/" + child.Platform.Architecture
		}

		children = append(children, platform)
	}

	if !slices.Equal(slices.Sorted(slices.Values(children)), slices.Sorted(slices.Values(platforms))) {
		return fmt.Errorf("holds the children %v, want exactly the planned platforms %v: %w", children, platforms, errs.ErrValidation)
	}

	return nil
}

// SnapshotEvidenceFindings judges the evidence a snapshot run retained, the
// tree of each image's evidence artifact by image name, against the expected
// plan and the receipts: what the plan's SBOM policy requires must be there,
// and what it forbids must not. A planned image retains one artifact, whose
// publication record (<image>-dist/<image>.json) names its receipt's tag,
// in its repository as the registry was given it, digest and reference; that record is what binds the directory's documents
// to the published image, the product writing no image identity of its own
// into them. Each planned platform holds its Trivy report, scanned whatever
// the policy, and sbom.cyclonedx.json exactly when the plan generates one, a
// CycloneDX document as the product's own inventory step admits it. Any other
// file is a finding, so an SBOM under another name or platform is not
// overlooked, as is an artifact of an image the plan does not have. It
// returns the findings, none for a run that kept its policy.
func SnapshotEvidenceFindings(plan snapshotimage.Plan, receipts []snapshotimage.Receipt, evidence map[string]map[string][]byte) []string {
	var findings []string

	planned := map[string]bool{}

	for _, image := range plan.Images {
		planned[image.Name] = true

		tree, retained := evidence[image.Name]
		if !retained {
			findings = append(findings, fmt.Sprintf("image %s retained no evidence artifact", image.Name))

			continue
		}

		record := image.Name + "-dist/" + image.Name + ".json"
		admitted := map[string]bool{record: true}

		var published appcontainer.SignerImageMetadata

		// A receipt keeps the repository and the tag apart; the record holds
		// the pushed reference (lab runs 610 and 621 to 629, 2026-11-10).
		receipt := slices.IndexFunc(receipts, func(receipt snapshotimage.Receipt) bool { return receipt.Name == image.Name })
		if err := json.Unmarshal(tree[record], &published); err != nil || receipt < 0 ||
			published != (appcontainer.SignerImageMetadata{
				Tag: receipts[receipt].ImageRepository + ":" + receipts[receipt].Tag, Digest: receipts[receipt].Digest, Ref: receipts[receipt].Ref,
			}) {
			findings = append(findings, fmt.Sprintf("the evidence of image %s is not bound to its receipt: %s records %+v", image.Name, record, published))
		}

		for _, platform := range image.Platforms {
			directory := image.Name + "/" + strings.ReplaceAll(platform, "/", "-") + "/"

			// A clean scan derives no SARIF or GitLab report.
			for _, document := range []string{"trivy.json", "trivy.sarif", "gitlab.json", "sbom.cyclonedx.json"} {
				admitted[directory+document] = true
			}

			for _, finding := range snapshotPlatformEvidenceFindings(image.SBOM, tree, directory) {
				findings = append(findings, fmt.Sprintf("image %s (%s) %s", image.Name, platform, finding))
			}
		}

		for _, path := range slices.Sorted(maps.Keys(tree)) {
			if !admitted[path] {
				findings = append(findings, fmt.Sprintf("image %s retained unplanned evidence %s", image.Name, path))
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(evidence)) {
		if !planned[name] {
			findings = append(findings, "evidence artifact of unplanned image "+name)
		}
	}

	return findings
}

// snapshotPlatformEvidenceFindings judges one platform's directory of an
// image's evidence: its Trivy report, and its SBOM as the plan has it.
func snapshotPlatformEvidenceFindings(sbom bool, tree map[string][]byte, directory string) []string {
	var findings []string

	var scan struct {
		SchemaVersion int `json:"SchemaVersion"`
	}
	if err := json.Unmarshal(tree[directory+"trivy.json"], &scan); err != nil || scan.SchemaVersion < 1 {
		findings = append(findings, "retained no Trivy report")
	}

	body, generated := tree[directory+"sbom.cyclonedx.json"]
	if !sbom || !generated {
		if sbom {
			findings = append(findings, "retained no SBOM, which its plan requires")
		}

		if generated {
			findings = append(findings, "retained an SBOM its plan forbids")
		}

		return findings
	}

	// What the product's own inventory step admits before it installs one.
	var document struct {
		Format      string `json:"bomFormat"`
		SpecVersion string `json:"specVersion"`
		Version     int    `json:"version"`
	}
	if err := json.Unmarshal(body, &document); err != nil || document.Format != "CycloneDX" || document.SpecVersion == "" || document.Version < 1 {
		findings = append(findings, "retained an SBOM that is not a CycloneDX document")
	}

	return findings
}
