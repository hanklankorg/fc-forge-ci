// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"context"
	_ "embed"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/container"
)

// A service development is a service snapshot's route on Forgejo, where the
// consumer's own workflow calls the snapshot-images policy: the harness writes
// that workflow, for a consumer of any snapshot family, over the staged
// declaration (servicesnapshot.go), then runs the published image for
// readiness. The producers the declaration names are built by the snapshot
// itself, in the family's image, which the repository's
// SNAPSHOT_ARTIFACT_JOB_IMAGE_<FAMILY> variable names
// (ServiceSnapshotJobImageVariables); a container-first service has none.

var (
	errServiceRecipeOrImage     = errors.New("name an absolute native recipe directory and a full digest-pinned job image")
	errServiceReadinessURL      = errors.New("readiness must be an explicit http://127.0.0.1:<port>/<project-endpoint> URL")
	errServiceReadinessPort     = errors.New("readiness requires a valid container port")
	errServiceSourceBranch      = errors.New("select a valid source branch different from policy main")
	errServiceFixtureRelative   = errors.New("fixture must be an absolute CI script path")
	errServiceFixtureNotRegular = errors.New("fixture must be a regular CI script")
	errServiceRecipeRegistry    = errors.New("the development recipe no longer names its registry as the files snapshot expects")
)

// serviceRecipeRegistry is what the development recipe passes for the image
// publication, which a snapshot of file outputs alone leaves out.
const serviceRecipeRegistry = `      image-repository: ${{ vars.SERVICE_IMAGE_REPOSITORY }}
      registry-user: ${{ vars.DEVELOPMENT_REGISTRY_USERNAME }}
    secrets:
      SNAPSHOT_REGISTRY_PASSWORD: ${{ secrets.DEVELOPMENT_REGISTRY_TOKEN }}
`

// ServiceDevelopmentConfig supplies trusted trial infrastructure: the demo
// recipe whose release workflow names the layer pin; the declaration the
// snapshot is taken from, ServiceSnapshotSource's, the default declaration
// when empty; the job image the snapshot builds the declaration's producers
// in; the source branch; the readiness URL; and Fixture, an optional CI-side
// script producing trial-runtime/runtime.env and optional files, run at the
// selected source on the container runner, never on the controller or in the
// source checkout. Images selects the snapshot event's scope, every image
// the planner plans from the declaration, in place of the lifecycle's one
// service; only the lifecycle checks readiness and reads Fixture. ReadinessURL
// and Fixture are ignored for snapshot-only events.
type ServiceDevelopmentConfig struct {
	RecipeDirectory, Declaration, JobImage, Branch, ReadinessURL, Fixture string
	Images                                                                bool
}

//go:embed testdata/service-development.yml.tmpl
var serviceDevelopmentRecipe string

// NewServiceDevelopmentFixture overlays the service development route in
// Consumer's private index alone: the service snapshot's declaration, the
// development workflow that takes it and checks the published image's
// readiness, and the fixture script. The declaration selects artifact-first
// or container-first; no byte equality of the image's content is claimed.
func NewServiceDevelopmentFixture(tb TB, source Consumer, config ServiceDevelopmentConfig) Consumer {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	fixture, err := serviceDevelopmentFixture(ctx, source, config)
	if err != nil {
		tb.Fatalf("service development fixture: %v", err)
	}

	return fixture
}

func serviceDevelopmentFixture(ctx context.Context, source Consumer, config ServiceDevelopmentConfig) (Consumer, error) {
	if err := config.validateInfrastructure(); err != nil {
		return source, err
	}

	if _, err := source.git.run(ctx, source.stage, "check-ref-format", "refs/heads/"+config.Branch); err != nil || config.Branch == defaultBranch || config.Branch == "" {
		return source, errServiceSourceBranch
	}

	scope := serviceScope
	if config.Images {
		scope = snapshotImagesScope
	}

	service, err := serviceSnapshotDeclaration(ctx, source, cmp.Or(config.Declaration, DefaultArtifactsConfig), scope)
	if err != nil {
		return source, err
	}

	var fixture string
	if !config.Images {
		fixture, err = readServiceFixture(config.Fixture)
		if err != nil {
			return source, err
		}
	}

	body, err := os.ReadFile(filepath.Join(config.RecipeDirectory, ".forgejo/workflows/release.yml"))
	if err != nil {
		return source, err
	}

	pin, err := ReadConsumerPin(map[string]string{trialReleaseWorkflow: string(body)})
	if err != nil {
		return source, err
	}
	// YAML quoting prevents branch/endpoint metadata becoming syntax.
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

	// A plan of file outputs alone names no registry and passes no secret:
	// the snapshot is asked for the files instead.
	recipe, err := developmentRecipe(config.Images && len(service.plan.Images) == 0)
	if err != nil {
		return source, err
	}

	workflow := strings.NewReplacer("@ACTIONS@", pin.Actions, "@WORKFLOWS@", pin.Workflows, "@PIN@", pin.Commit,
		"@BRANCH@", quote(config.Branch), "@URL@", quote(config.ReadinessURL)).Replace(recipe)
	// Snapshot events verify published images, not service readiness. An
	// image count cannot tell whether an image is a runnable service.
	if config.Images {
		workflow, err = withoutReadinessJob(workflow)
		if err != nil {
			return source, err
		}
	}

	staged, err := service.stageFiles(ctx, map[string]string{
		forgejoDevelopmentWorkflow:     workflow,
		ServiceSnapshotDeclarationFile: service.declaration,
		".forge-ci/service-fixture.sh": fixture,
	})

	return staged.Consumer, err
}

// developmentRecipe is the development workflow's recipe; for a snapshot of
// file outputs alone it asks for the files where it would name the registry.
func developmentRecipe(filesOnly bool) (string, error) {
	if !filesOnly {
		return serviceDevelopmentRecipe, nil
	}

	if !strings.Contains(serviceDevelopmentRecipe, serviceRecipeRegistry) {
		return "", errServiceRecipeRegistry
	}

	return strings.Replace(serviceDevelopmentRecipe, serviceRecipeRegistry, "      publish-files: true\n", 1), nil
}

// withoutReadinessJob is the workflow without its readiness job.
func withoutReadinessJob(workflow string) (string, error) {
	var document struct {
		Fields map[string]yaml.Node `yaml:",inline"`
		Jobs   map[string]yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		return "", err
	}

	delete(document.Jobs, "readiness")

	return encodeYAML(document)
}

// ServiceSnapshotJobImageVariables names the image the snapshot builds the
// consumer's producers in: one repository variable per family of the staged
// declaration's producers, SNAPSHOT_ARTIFACT_JOB_IMAGE_<FAMILY>, which the
// snapshot-images workflow reads where its caller passes no
// artifact-job-image-<family> input (docs/forgejo.md, Snapshots). A
// container-first service has no producer and takes none. The image is the
// family's own where the trial's variables name one, CI_JOB_IMAGE_<FAMILY>,
// as a case with a NodeJobImage does for the node family, and the trial's
// job image, CI_JOB_IMAGE, for every other family.
func ServiceSnapshotJobImageVariables(ctx context.Context, tb TB, consumer Consumer, trial map[string]string) map[string]string {
	tb.Helper()

	variables := map[string]string{}

	for _, family := range ServiceSnapshotFamilies(ctx, tb, consumer) {
		name := strings.ToUpper(family)
		variables["SNAPSHOT_ARTIFACT_JOB_IMAGE_"+name] = cmp.Or(trial[lifecycleJobImage+"_"+name], trial[lifecycleJobImage])
	}

	return variables
}

// validateInfrastructure checks the recipe directory, the job image and the
// lifecycle's readiness endpoint before any Git work. A snapshot needs no
// endpoint because it judges publication, not a runnable service.
func (c ServiceDevelopmentConfig) validateInfrastructure() error {
	if !filepath.IsAbs(c.RecipeDirectory) || !container.ValidDigestReference(c.JobImage) {
		return errServiceRecipeOrImage
	}

	if c.Images {
		return nil
	}

	endpoint, err := url.Parse(c.ReadinessURL)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.Path == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return errServiceReadinessURL
	}

	if port, err := strconv.Atoi(endpoint.Port()); err != nil || port < 1 || port > 65535 {
		return errServiceReadinessPort
	}

	return nil
}

// readServiceFixture reads the optional operator-supplied CI script; an
// empty path yields an empty script.
func readServiceFixture(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	if !filepath.IsAbs(path) {
		return "", errServiceFixtureRelative
	}

	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errServiceFixtureNotRegular
	}

	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied fixture, checked above as an absolute regular file.
	if err != nil {
		return "", err
	}

	return string(body), nil
}

const (
	// ServiceDevelopmentWorkflow is the harness's development workflow, which
	// NewServiceDevelopmentFixture writes and DispatchConsumerDevelopment
	// dispatches on main.
	ServiceDevelopmentWorkflow = "development.yml"
	// ForgejoSnapshotFilesWorkflow is the Forgejo caller a demo ships for a
	// snapshot of its files: it snapshots the branch it is dispatched on,
	// from the default declaration.
	ForgejoSnapshotFilesWorkflow = "snapshot-files.yml"
)

// ForgejoSnapshotRoute names the workflow a Forgejo snapshot dispatches, the
// ref it dispatches it on, and the declaration that workflow reads. A
// snapshot of files from the default declaration of a consumer that ships
// ForgejoSnapshotFilesWorkflow runs that caller on the branch to snapshot,
// as its reader would, and the caller reads DefaultArtifactsConfig; any
// other snapshot runs the harness's workflow on main, which names the branch
// and reads the staged ServiceSnapshotDeclarationFile.
func ForgejoSnapshotRoute(workflows map[string]string, files bool, declaration, branch string) (string, string, string) {
	if _, shipped := workflows[ForgejoSnapshotFilesWorkflow]; shipped && files && declaration == DefaultArtifactsConfig {
		return ForgejoSnapshotFilesWorkflow, branch, DefaultArtifactsConfig
	}

	return ServiceDevelopmentWorkflow, defaultBranch, ServiceSnapshotDeclarationFile
}

// DispatchConsumerDevelopment dispatches policy main once; selected source is
// resolved by the shared snapshot planner, not by this controller.
func DispatchConsumerDevelopment(tb TB, target Target, repo string) {
	tb.Helper()

	DispatchConsumerWorkflow(tb, target, repo, ServiceDevelopmentWorkflow, defaultBranch)
}

// DispatchConsumerWorkflow dispatches the consumer's workflow once, on ref.
func DispatchConsumerWorkflow(tb TB, target Target, repo, workflow, ref string) {
	tb.Helper()
	requireAccepted(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(repo) || !regexp.MustCompile(`^[A-Za-z0-9_-]+\.yml$`).MatchString(workflow) {
		tb.Fatalf("invalid scratch repository or workflow")
	}

	err := discard(ctx, target, http.MethodPost, actionsURL(target, repo, "/actions/workflows/"+workflow+"/dispatches"), map[string]string{"ref": ref}, http.StatusNoContent)
	if err != nil {
		tb.Fatalf("dispatch %s on %s: %v", workflow, ref, err)
	}
}
