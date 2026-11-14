// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"gopkg.in/yaml.v3"
)

// A service snapshot is the development route whose pipeline the forge's
// snapshot-images policy owns, the reusable workflow on Forgejo and the
// component on GitLab, over the consumer's own declaration of one container.
// The trial overlays that declaration, native linux/amd64 and unsigned, into
// ServiceSnapshotDeclarationFile and judges the snapshot by the frozen plan
// and the receipts the route leaves. What a snapshot builds is the planner's
// rule (snapshotimage.New), and the harness keeps no copy of it: a
// declaration is admitted by planning it. The harness adds its own acceptance
// scope alone, serviceSnapshotScope. A consumer whose declaration the planner
// refuses has no development route, and a lifecycle refuses it.
//
// The snapshot event takes the same route over every image the planner
// plans from the declaration, under snapshotImagesScope: the trial judges
// each planned image's receipt, and the plan, not the case, says which
// images those are.

var (
	errServiceDeclaration = errors.New("the trial judges one container of the root Containerfile and context, planned for amd64, without image name, tag suffix or declared provenance, from at most one artifact")
	errSnapshotImages     = errors.New("the trial judges every planned image in the run's one image repository, each planned for amd64, without image name or declared provenance")
)

// snapshotScope is a trial's acceptance scope over a declaration the planner
// plans: the declarations it admits, and its refusal of the others.
type snapshotScope struct {
	admits  func(cfg *config.Config) bool
	refusal error
	// rootContainerfile is whether the scope builds from the root
	// Containerfile, which a consumer must then have.
	rootContainerfile bool
	// files is whether the scope also takes a declaration without a
	// container, for its file outputs: the snapshot is then asked for files
	// (publish-files) and judged by the bundles its run keeps.
	files bool
}

var (
	// serviceScope is the lifecycle's: one service, serviceSnapshotScope.
	serviceScope = snapshotScope{
		admits:            func(cfg *config.Config) bool { return serviceSnapshotScope(cfg) == nil },
		refusal:           errServiceDeclaration,
		rootContainerfile: true,
	}
	// snapshotImagesScope is the snapshot event's: every planned image, or,
	// of a declaration without a container, every planned file output.
	snapshotImagesScope = snapshotScope{admits: snapshotImagesAdmitted, refusal: errSnapshotImages, files: true}
)

// ServiceSnapshotDeclarationFile is the consumer path of the service
// snapshot's declaration: the consumer's own declaration, overlaid for the
// trial's native platform and unsigned policy. A development route names it
// as its artifacts config.
const ServiceSnapshotDeclarationFile = ".forge-ci/service-development.yml"

// ServiceSnapshotSource names the declaration a consumer's service snapshot
// is taken from: the dedicated release-images declaration when the consumer
// has one, since its releases build their images from it, else the default.
func ServiceSnapshotSource(consumerRoot string) (string, error) {
	_, dedicated, err := GitLabReleaseImages(consumerRoot)
	if err != nil {
		return "", err
	}

	return cmp.Or(dedicated, DefaultArtifactsConfig), nil
}

// ServiceSnapshotOverlay is the service snapshot's declaration of the
// consumer checkout at root, as every forge's lifecycle stages it at
// ServiceSnapshotDeclarationFile: the release declaration
// (ServiceSnapshotSource) at HEAD, overlaid for the trial's native platform
// and unsigned policy, a snapshot's images being unsigned. The hosted
// lifecycle stages it into the tree it pushes, where the lab's development
// routes stage it into the consumer's.
func ServiceSnapshotOverlay(tb TB, root string) string {
	tb.Helper()

	source, err := ServiceSnapshotSource(root)
	if err != nil {
		tb.Fatalf("service snapshot source: %v", err)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutScratch)
	defer cancel()

	git := githubStageGit()
	if _, err := git.output(ctx, root, nil, "cat-file", "-e", "HEAD:Containerfile"); err != nil {
		tb.Fatalf("service snapshot: the consumer has no root Containerfile at HEAD: %v", err)
	}

	body, err := git.output(ctx, root, nil, "show", "HEAD:"+source)
	if err != nil {
		tb.Fatalf("service snapshot declaration %s: %v", source, err)
	}

	declaration, _, err := serviceSnapshotDeclarationOverlay(string(body))
	if err != nil {
		tb.Fatalf("service snapshot declaration %s: %v", source, err)
	}

	return declaration
}

// serviceSnapshot is a consumer admitted for a service snapshot: its
// declaration overlaid for the trial, and the plan the planner makes of it.
type serviceSnapshot struct {
	Consumer

	declaration string
	plan        snapshotimage.Plan
}

// serviceSnapshotDeclaration admits the consumer for a snapshot in scope: at
// source a declaration the planner plans, and the root Containerfile where
// the scope builds from it.
func serviceSnapshotDeclaration(ctx context.Context, consumer Consumer, source string, scope snapshotScope) (serviceSnapshot, error) {
	if scope.rootContainerfile {
		if _, err := githubWorkflow(ctx, consumer, "Containerfile"); err != nil {
			return serviceSnapshot{}, err
		}
	}

	declared, err := githubWorkflow(ctx, consumer, source)
	if err != nil {
		return serviceSnapshot{}, err
	}

	declaration, plan, err := snapshotDeclarationOverlay(declared, scope)
	if err != nil {
		return serviceSnapshot{}, err
	}

	return serviceSnapshot{Consumer: consumer, declaration: declaration, plan: plan}, nil
}

// stageFiles commits the files onto the admitted tree, in the consumer's
// private index alone.
func (snapshot serviceSnapshot) stageFiles(ctx context.Context, files map[string]string) (serviceSnapshot, error) {
	if _, err := snapshot.git.run(ctx, snapshot.stage, "read-tree", snapshot.Commit); err != nil {
		return snapshot, err
	}

	for path, text := range files {
		if err := snapshot.stageFile(ctx, "100644", path, text); err != nil {
			return snapshot, err
		}
	}

	commit, err := snapshot.commit(ctx, consumerBaseMessage)
	if err != nil {
		return snapshot, err
	}

	snapshot.Commit = commit

	return snapshot, nil
}

// WithServiceSnapshotDeclaration returns the consumer with the service
// snapshot's declaration in its tree, ServiceSnapshotDeclarationFile overlaid
// from source, and nothing else: a route whose pipeline the forge's policy
// owns, as GitLab's snapshot-images component, reads the declaration from the
// consumer and needs no workflow of the consumer's.
func (consumer Consumer) WithServiceSnapshotDeclaration(tb TB, source string) Consumer {
	tb.Helper()

	return consumer.withSnapshotDeclaration(tb, source, serviceScope)
}

// WithSnapshotImagesDeclaration is WithServiceSnapshotDeclaration for the
// snapshot event: the declaration overlaid for every image the planner plans
// from it (snapshotImagesScope).
func (consumer Consumer) WithSnapshotImagesDeclaration(tb TB, source string) Consumer {
	tb.Helper()

	return consumer.withSnapshotDeclaration(tb, source, snapshotImagesScope)
}

func (consumer Consumer) withSnapshotDeclaration(tb TB, source string, scope snapshotScope) Consumer {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	declared, err := serviceSnapshotDeclaration(ctx, consumer, source, scope)
	if err == nil {
		declared, err = declared.stageFiles(ctx, map[string]string{ServiceSnapshotDeclarationFile: declared.declaration})
	}

	if err != nil {
		tb.Fatalf("service snapshot declaration: %v", err)
	}

	return declared.Consumer
}

// ServiceSnapshotFamilies lists the toolchain families of the producers the
// consumer's staged snapshot declaration builds its container from, each
// once, sorted: none for a container-first service, whose Containerfile
// builds the application itself.
func ServiceSnapshotFamilies(ctx context.Context, tb TB, consumer Consumer) []string {
	tb.Helper()

	in, err := serviceSnapshotStandInInput()
	if err != nil {
		tb.Fatalf("service plan input: %v", err)
	}

	return serviceSnapshotFamilies(ServiceSnapshotPlan(ctx, tb, consumer, in))
}

// SnapshotFileOutputs names the file outputs the consumer's staged snapshot
// declaration plans, sorted: none for a declaration with a container, whose
// snapshot the trial judges by its images.
func SnapshotFileOutputs(ctx context.Context, tb TB, consumer Consumer) []string {
	tb.Helper()

	in, err := serviceSnapshotStandInInput()
	if err != nil {
		tb.Fatalf("service plan input: %v", err)
	}

	files := ServiceSnapshotPlan(ctx, tb, consumer, in).Files

	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.Name)
	}

	slices.Sort(names)

	return names
}

// ServiceSnapshotPlan reads the staged declaration through the same Git
// reader as the fixtures, then plans it with the shared planner. Acceptance
// must not assume that every service is container-first.
func ServiceSnapshotPlan(ctx context.Context, tb TB, consumer Consumer, in snapshotimage.Input) snapshotimage.Plan {
	tb.Helper()

	plan, err := planSnapshotDeclaration(ServiceSnapshotDeclaration(ctx, tb, consumer), in)
	if err != nil {
		tb.Fatalf("service plan: %v", err)
	}

	return plan
}

// ServiceSnapshotDeclaration reads the consumer's staged snapshot
// declaration, the one its development run plans.
func ServiceSnapshotDeclaration(ctx context.Context, tb TB, consumer Consumer) string {
	tb.Helper()

	return ConsumerDeclaration(ctx, tb, consumer, ServiceSnapshotDeclarationFile)
}

// ConsumerDeclaration reads the declaration at path in the consumer's tree.
func ConsumerDeclaration(ctx context.Context, tb TB, consumer Consumer, path string) string {
	tb.Helper()

	body, err := githubWorkflow(ctx, consumer, path)
	if err != nil {
		tb.Fatalf("declaration %s: %v", path, err)
	}

	return body
}

// planSnapshotDeclaration plans a declaration's body with the shared
// planner, as the product's snapshot plan step does. The trial asks for the
// file outputs of a declaration without a container, and of no other.
func planSnapshotDeclaration(body string, in snapshotimage.Input) (snapshotimage.Plan, error) {
	cfg, err := parseServiceDeclaration(body)
	if err != nil {
		return snapshotimage.Plan{}, err
	}

	in.PublishFiles = len(cfg.Containers) == 0

	return snapshotimage.New(pipeline.NewConfigPlan(cfg), in)
}

// serviceSnapshotStandInInput is the planner input a declaration is admitted
// with before the trial has a source: the planner's rules on a declaration do
// not depend on the source identity, so a stand-in one plans it.
func serviceSnapshotStandInInput() (snapshotimage.Input, error) {
	const (
		shortSHA = "aaaaaaa"
		branch   = "development"
	)

	tag, err := version.ComposeSnapshotContainerTag("0.0.0", branch, shortSHA)
	if err != nil {
		return snapshotimage.Input{}, err
	}

	return snapshotimage.Input{
		SourceSHA: shortSHA + strings.Repeat("a", 33), SourceBranch: branch, Tag: tag,
		ImageRepository: "registry.invalid/service/snapshot", SourceDateEpoch: "1", Publish: true,
	}, nil
}

// serviceSnapshotDeclarationOverlay is snapshotDeclarationOverlay in the
// lifecycle's scope, one service.
func serviceSnapshotDeclarationOverlay(body string) (string, snapshotimage.Plan, error) {
	return snapshotDeclarationOverlay(body, serviceScope)
}

// snapshotDeclarationOverlay preserves the declared build graph, the raw
// ecosystem settings and the release's sign and git-signing blocks, so the
// snapshot reads a declaration that signs its release, and asks for
// provenance where it does, and leaves both out. Only the trial's native
// platform is overlaid. A row whose demo declares sign or git-signing, as
// hello-go-service does, proves the route tolerates them; that it ignores
// a container's enable-slsa is proven by every row whose container leaves
// the release's default on, and by the planner's unit tests.
// The overlay is then planned as the snapshot will plan it, and the
// planner's refusal is the declaration's; the scope's refusal is the
// trial's. It returns the overlay and its plan.
func snapshotDeclarationOverlay(body string, scope snapshotScope) (string, snapshotimage.Plan, error) {
	cfg, err := parseServiceDeclaration(body)
	if err != nil {
		return "", snapshotimage.Plan{}, err
	}

	if !scope.admits(cfg) {
		return "", snapshotimage.Plan{}, scope.refusal
	}

	in, err := serviceSnapshotStandInInput()
	if err != nil {
		return "", snapshotimage.Plan{}, err
	}

	// A declaration without a container is snapshotted as it is, for its
	// file outputs: nothing of it is overlaid.
	if scope.files && len(cfg.Containers) == 0 {
		return filesSnapshotDeclaration(body, cfg, in, scope.refusal)
	}

	// The planner judges the declaration's snapshot, with the platforms as
	// declared, before the overlay narrows them.
	original, err := snapshotimage.New(pipeline.NewConfigPlan(cfg), in)
	if err != nil {
		return "", snapshotimage.Plan{}, err
	}

	if len(original.Images) == 0 || slices.ContainsFunc(original.Images, func(image snapshotimage.Image) bool {
		return !slices.Contains(image.Platforms, build.DefaultPlatform)
	}) {
		return "", snapshotimage.Plan{}, scope.refusal
	}

	for i := range cfg.Containers {
		cfg.Containers[i].Platforms = build.DefaultPlatform
	}

	overlay, err := encodeSnapshotOverlay(body, cfg.Containers)
	if err != nil {
		return "", snapshotimage.Plan{}, err
	}
	// An alias whose anchor lived in an omitted release-policy block must not
	// leave an invalid private declaration behind.
	overlaid, err := parseServiceDeclaration(overlay)
	if err != nil {
		return "", snapshotimage.Plan{}, err
	}

	plan, err := snapshotimage.New(pipeline.NewConfigPlan(overlaid), in)
	if err != nil {
		return "", snapshotimage.Plan{}, err
	}

	return overlay, plan, nil
}

// filesSnapshotDeclaration is the declaration as it is and its plan of file
// outputs; a plan without one is the scope's refusal.
func filesSnapshotDeclaration(body string, cfg *config.Config, in snapshotimage.Input, refusal error) (string, snapshotimage.Plan, error) {
	in.PublishFiles = true

	plan, err := snapshotimage.New(pipeline.NewConfigPlan(cfg), in)
	if err == nil && len(plan.Files) == 0 {
		err = refusal
	}

	if err != nil {
		return "", snapshotimage.Plan{}, err
	}

	return body, plan, nil
}

// encodeSnapshotOverlay is the declaration with its containers replaced.
// Artifact's typed ecosystem fields are not YAML-marshallable, so their
// original nodes are kept, after the strict shared parser has admitted the
// whole input.
func encodeSnapshotOverlay(body string, containers []config.Container) (string, error) {
	var raw struct {
		Artifacts  yaml.Node `yaml:"artifacts,omitempty"`
		Sign       yaml.Node `yaml:"sign,omitempty"`
		GitSigning yaml.Node `yaml:"git-signing,omitempty"`
	}
	if err := yaml.Unmarshal([]byte(body), &raw); err != nil {
		return "", err
	}

	return encodeYAML(struct {
		Sign       yaml.Node          `yaml:"sign,omitempty"`
		GitSigning yaml.Node          `yaml:"git-signing,omitempty"`
		Artifacts  yaml.Node          `yaml:"artifacts,omitempty"`
		Containers []config.Container `yaml:"containers"`
	}{raw.Sign, raw.GitSigning, raw.Artifacts, containers})
}

// serviceSnapshotScope is the trial's acceptance scope, not a product rule:
// the planner decides what a snapshot builds; the trial judges one image,
// built from the root Containerfile and context, under the repository and
// tag it derives, so it takes one container without image name or tag suffix,
// from at most one artifact. The overlay narrows the platforms to native
// amd64, which the planned declaration must therefore include. Provenance
// is the planner's to leave out, declared or by the release's default.
func serviceSnapshotScope(cfg *config.Config) error {
	if len(cfg.Containers) != 1 {
		return errServiceDeclaration
	}

	ctr := cfg.Containers[0]
	selected := pipeline.NewConfigPlan(cfg).Containers.All[0]

	if ctr.ImageName != "" || ctr.TagSuffix != "" || len(ctr.From) > 1 ||
		selected.ContainerFile != "Containerfile" || selected.Context != "." {
		return errServiceDeclaration
	}

	return nil
}

// snapshotImagesAdmitted is the snapshot event's acceptance scope, not a
// product rule: the planner decides what a snapshot builds, and the trial
// judges every image it plans, each under the run's one image repository,
// so no container names an image of its own. The overlay narrows every
// image's platforms to native amd64.
func snapshotImagesAdmitted(cfg *config.Config) bool {
	return !slices.ContainsFunc(cfg.Containers, func(ctr config.Container) bool {
		return ctr.ImageName != ""
	})
}

// serviceSnapshotFamilies lists the toolchain families of a plan's producers,
// each once, sorted.
func serviceSnapshotFamilies(plan snapshotimage.Plan) []string {
	families := []string{}

	for _, artifact := range slices.Concat(plan.Artifacts, plan.Files) {
		// The planner admitted every producer by this mapping.
		if family, err := snapshotimage.ToolchainFamily(artifact.ProjectType); err == nil {
			families = append(families, family)
		}
	}

	slices.Sort(families)

	return slices.Compact(families)
}

func parseServiceDeclaration(body string) (*config.Config, error) {
	cfg, err := config.Parse([]byte(body))
	if err != nil {
		return nil, err
	}

	if err = config.Validate(cfg); err != nil {
		return nil, err
	}

	if err = config.Derive(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}
