// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

// These are overlay/plan regressions, not native SBOM-generation evidence.
// Both existing snapshot routes stage this overlay; policy must survive it
// without acquiring a second producer for a container-first consumer.
func TestServiceSnapshotDeclarationSBOMPolicies(t *testing.T) {
	for _, mode := range []string{"artifact-first", "container-first"} {
		for _, policy := range []struct {
			name, declaration string
			layers            []config.SBOMLayer
			container         bool
		}{
			{"default", "", []config.SBOMLayer{config.SBOMLayerBuild, config.SBOMLayerAnalyzedArtifact, config.SBOMLayerAnalyzedContainer}, true},
			{"all", "all", []config.SBOMLayer{config.SBOMLayerBuild, config.SBOMLayerAnalyzedArtifact, config.SBOMLayerAnalyzedContainer}, true},
			{"none", "none", nil, false},
			{"build", "build", []config.SBOMLayer{config.SBOMLayerBuild}, false},
			{"mixed", "build,analyzed-container", []config.SBOMLayer{config.SBOMLayerBuild, config.SBOMLayerAnalyzedContainer}, true},
		} {
			t.Run(mode+"/"+policy.name, func(t *testing.T) {
				setting := ""
				if policy.declaration != "" {
					setting = fmt.Sprintf("sboms: %q, ", policy.declaration)
				}

				body := fmt.Sprintf("artifacts: [{name: app, project-type: go, %sconfig: {build-mode: %s, main-package: ./cmd/app, binary-name: app}}]\ncontainers: [{name: service, from: [app]}]\n", setting, mode)

				overlay, plan, err := serviceSnapshotDeclarationOverlay(body)
				if err != nil {
					t.Fatal(err)
				}

				before, after := serviceTestConfigPlan(t, body), serviceTestConfigPlan(t, overlay)
				if !reflect.DeepEqual(before.Artifacts, after.Artifacts) || !slices.Equal(after.Artifacts.All[0].EffectiveSBOMs, policy.layers) {
					t.Fatalf("overlay changed required/forbidden layers: %s", overlay)
				}

				if len(plan.Images) != 1 || plan.Images[0].SBOM != policy.container || !plan.Images[0].Scan || plan.Images[0].Provenance {
					t.Fatalf("snapshot image evidence policy changed: %+v", plan.Images)
				}

				wantProducers := 0
				if mode == "artifact-first" {
					wantProducers = 1
				}

				if len(plan.Artifacts) != wantProducers || len(plan.Builds) != 1 || len(plan.Builds[0].RequiredArtifacts) != wantProducers {
					t.Fatalf("missing producer or forbidden eager producer: %+v", plan)
				}
			})
		}
	}
}

// The release inclusion input is not the snapshot overlay's generation policy.
// In particular none suppresses SBOM downloads and container-first SBOM jobs,
// but must not erase artifact-first Build layers or container generation.
func TestServiceSnapshotSBOMPolicyReleaseBoundary(t *testing.T) {
	for _, mode := range []string{"artifact-first", "container-first"} {
		for _, selection := range []struct {
			name, value      string
			build, container bool
		}{
			{"all", "all", true, true},
			{"none", "none", false, false},
			{"build", "build", true, false},
			{"mixed", "build,analyzed-container", true, true},
		} {
			t.Run(mode+"/"+selection.name, func(t *testing.T) {
				body := fmt.Sprintf("artifacts: [{name: app, project-type: go, config: {build-mode: %s, main-package: ./cmd/app, binary-name: app}}]\ncontainers: [{name: service, from: [app]}]\n", mode)

				overlay, snapshot, err := serviceSnapshotDeclarationOverlay(body)
				if err != nil {
					t.Fatal(err)
				}

				cfg := serviceTestConfigPlan(t, overlay)
				// The fixture names no signing method, and the overlay keeps the
				// release's default provenance, which a release then refuses.
				cfg.Containers.All[0].EnableSLSA = false

				release, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{ConfigPlan: cfg, ReleaseSBOMs: selection.value})
				if err != nil {
					t.Fatal(err)
				}

				wantLayers, err := config.ExpandSBOMs(selection.value)
				if err != nil {
					t.Fatal(err)
				}

				gotLayers, err := config.ExpandSBOMs(release.Policy.SBOMs)
				if err != nil || !slices.Equal(gotLayers, wantLayers) || release.Policy.SBOMConflict != nil {
					t.Fatalf("release inclusion changed required/forbidden layers: %+v, %v", release.Policy, err)
				}

				// Exact inventory checks enforce absence as well as presence.
				var got []pipeline.ArtifactTransfer

				for _, item := range release.ArtifactTransfers.Items {
					if item.Kind == pipeline.ArtifactTransferBuildSBOM || item.Kind == pipeline.ArtifactTransferAnalyzedContainerSBOM {
						got = append(got, item)
					}
				}

				var want []pipeline.ArtifactTransfer
				if selection.build {
					want = append(want, pipeline.ArtifactTransfer{Kind: pipeline.ArtifactTransferBuildSBOM, Name: cfg.Artifacts.All[0].BuildSBOMArtifactName, Path: "./release-artifacts/", Required: true})
				}

				if selection.container {
					want = append(want, pipeline.ArtifactTransfer{Kind: pipeline.ArtifactTransferAnalyzedContainerSBOM, NameTemplate: "analyzed-container-sbom-{run_id}-service-amd64", Path: "./sbom-artifacts/", Required: true})
				}

				if !slices.Equal(got, want) {
					t.Fatalf("SBOM transfers = %+v, want exactly %+v", got, want)
				}

				artifactFirst := mode == "artifact-first"
				if release.Stages.Build.Targets.Go.Runs != artifactFirst || release.Stages.Publish.Targets.GoContainerFirst.Runs != (!artifactFirst && selection.build) {
					t.Fatalf("wrong build/SBOM job placement: %+v", release.Stages)
				}

				wantSBOMJobs := 0
				if !artifactFirst && selection.build {
					wantSBOMJobs = 1
				}

				if len(release.Stages.Publish.Targets.GoContainerFirst.Items) != wantSBOMJobs {
					t.Fatalf("missing or forbidden container-first SBOM target: %+v", release.Stages.Publish.Targets.GoContainerFirst)
				}

				if !slices.Contains(cfg.Artifacts.All[0].EffectiveSBOMs, config.SBOMLayerBuild) || !cfg.Containers.All[0].EnableAnalyzedContainerSBOM || !snapshot.Images[0].SBOM {
					t.Fatal("release inclusion rewrote per-artifact or snapshot generation policy")
				}
			})
		}
	}
}

func TestServiceSnapshotDeclarationPreservesBuildSettings(t *testing.T) {
	body := `artifacts:
  - name: app
    project-type: maven
    working-directory: .
    build-type: application
    sboms: none
    config:
      java-version: '25'
      maven-profile: service-checks
      settings-path: development/settings.xml
containers:
  - name: service
    from: [app]
    target: runtime
    platforms: linux/amd64,linux/arm64
    build-args:
      SERVICE_MODE: normal
    scan-severity: HIGH
`

	overlay, plan, err := serviceSnapshotDeclarationOverlay(body)
	if err != nil {
		t.Fatal(err)
	}

	if families := serviceSnapshotFamilies(plan); !slices.Equal(families, []string{snapshotimage.FamilyJava}) {
		t.Fatalf("families %v, want the Maven application's java", families)
	}

	before := serviceTestConfigPlan(t, body)
	after := serviceTestConfigPlan(t, overlay)

	if !reflect.DeepEqual(before.Artifacts, after.Artifacts) {
		t.Fatalf("producer settings changed: %s", overlay)
	}

	before.Containers.All[0].Platforms = "linux/amd64"

	if !reflect.DeepEqual(before.Containers, after.Containers) {
		t.Fatalf("container build settings changed: %s", overlay)
	}
	// The consumer's own yamlfmt, two-space, lints the staged declaration.
	if strings.Contains(overlay, "\n    - ") || !strings.Contains(overlay, "\n  - name: service\n") {
		t.Fatalf("overlay is not two-space indented YAML:\n%s", overlay)
	}
}

// The plan is the planner's: even a declared artifact is not a producer
// unless the image depends on it, and a container-first artifact is built by
// the Containerfile. Container-first consumers must not acquire an eager
// extra build.
func TestServiceSnapshotDeclarationContainerOnlyHasNoProducer(t *testing.T) {
	for _, declaration := range []string{
		"containers: [{name: service}]\n",
		"artifacts: [{name: unused, project-type: maven, build-type: application}]\ncontainers: [{name: service}]\n",
		"artifacts: [{name: service, project-type: cargo, config: {build-mode: container-first}}]\ncontainers: [{name: service, from: [service]}]\n",
	} {
		_, plan, err := serviceSnapshotDeclarationOverlay(declaration)
		if err != nil {
			t.Fatal(err)
		}

		if families := serviceSnapshotFamilies(plan); len(families) != 0 || len(plan.Artifacts) != 0 || len(plan.Builds) != 1 || len(plan.Builds[0].RequiredArtifacts) != 0 {
			t.Fatalf("container-first acquired an artifact producer: %v %+v", families, plan)
		}
	}
}

// The snapshot builds a root producer of any family it has an image for, on
// both forges' routes.
func TestServiceSnapshotDeclarationAdmitsEveryProducerFamily(t *testing.T) {
	for family, artifact := range map[string]string{
		snapshotimage.FamilyJava: "{name: app, project-type: maven, build-type: application}",
		snapshotimage.FamilyNode: "{name: app, project-type: npm}",
		snapshotimage.FamilyGo:   "{name: app, project-type: go, config: {build-mode: artifact-first, main-package: ./cmd/app, binary-name: app}}",
		snapshotimage.FamilyRust: "{name: app, project-type: cargo, config: {build-mode: artifact-first}}",
	} {
		t.Run(family, func(t *testing.T) {
			_, plan, err := serviceSnapshotDeclarationOverlay("artifacts: [" + artifact + "]\ncontainers: [{name: service, from: [app]}]\n")
			if err != nil {
				t.Fatal(err)
			}

			if families := serviceSnapshotFamilies(plan); !slices.Equal(families, []string{family}) {
				t.Fatalf("families %v, want %s", families, family)
			}
		})
	}
}

// A refusal is the planner's where the planner has the rule, and the trial's
// acceptance scope's where the trial has it; neither falls back to another
// route.
func TestServiceSnapshotDeclarationRefusesUnsupportedInputs(t *testing.T) {
	const scope = "the trial judges one container"

	for name, test := range map[string]struct{ body, want string }{
		"missing declaration":      {"", "empty input"},
		"no container":             {"artifacts: [{name: app, project-type: maven}]", scope},
		"unknown dependency":       {"containers: [{name: service, from: [missing]}]", "missing"},
		"unknown setting":          {"containers: [{name: service, bogus: true}]", "bogus"},
		"multiple services":        {"containers: [{name: a}, {name: b}]", scope},
		"no native amd64":          {"containers: [{name: service, platforms: linux/arm64}]", scope},
		"unsupported architecture": {"containers: [{name: service, platforms: 'linux/amd64,linux/arm/v7'}]", "snapshot requires native"},
		"duplicate platform":       {"containers: [{name: service, platforms: 'linux/amd64,linux/amd64'}]", "duplicate"},
		"other context":            {"containers: [{name: service, context: subdir}]", scope},
		"other file":               {"containers: [{name: service, container-file: Otherfile}]", scope},
		"other image":              {"containers: [{name: service, image-name: other/image}]", scope},
		"two producers":            {"artifacts: [{name: a, project-type: maven, build-type: application}, {name: b, project-type: npm}]\ncontainers: [{name: service, from: [a, b]}]", scope},
		"build secret":             {"containers: [{name: service, build-secrets: [TOKEN]}]", "declares build-secrets"},
	} {
		t.Run(name, func(t *testing.T) {
			overlay, _, err := serviceSnapshotDeclarationOverlay(test.body)
			if err == nil || overlay != "" || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsupported declaration: %q, %v; want a refusal naming %q", overlay, err, test.want)
			}
		})
	}
}

// The demos' own declarations decide which has a development route: the one
// ServiceSnapshotSource names, as the GitLab lifecycle and the demo
// admission read it. A demo whose declaration the snapshot cannot build has
// none, and the reason is the planner's, not a name's.
func TestServiceSnapshotSourceOfTheDemos(t *testing.T) {
	for demo, want := range map[string]struct {
		source   string
		families []string
		refused  string
		// sbom is the image SBOM policy the trial's evidence oracle then holds
		// the native run to (SnapshotEvidenceFindings): generated by default
		// for a container built from an artifact, and forbidden for one that
		// names none or whose artifact requests no analyzed-container layer.
		// With families, artifact-first where a producer is planned, the rows
		// are the four cells: either route, with the image SBOM and without.
		sbom bool
		// variant is a declaration a snapshot trial selects in place of the
		// source (snapshot-declaration=).
		variant string
	}{
		"hello-maven-service": {source: DefaultArtifactsConfig, families: []string{snapshotimage.FamilyJava}, sbom: true},
		// Gradle is a producer of the same family, its jar in build/libs.
		"hello-gradle-service": {source: DefaultArtifactsConfig, families: []string{snapshotimage.FamilyJava}, sbom: true},
		// Artifact-first without: the same producer, Build SBOM only.
		"hello-maven-service/build-sbom": {source: DefaultArtifactsConfig, variant: mavenServiceBuildSBOMDeclaration, families: []string{snapshotimage.FamilyJava}},
		"hello-npm-service":              {source: DefaultArtifactsConfig, families: []string{snapshotimage.FamilyNode}, sbom: true},
		"hello-cargo-container":          {source: GitLabDedicatedReleaseImagesConfig, families: []string{}},
		// Two runtime images: the trial judges one container.
		"hello-cargo-workspace": {source: GitLabDedicatedReleaseImagesConfig, refused: "one container"},
		// Container-first with a release's extraction, which a snapshot
		// does not run: no producer.
		"hello-go-service": {source: DefaultArtifactsConfig, families: []string{}, sbom: true},
		// No container, so no snapshot.
		"hello-go-cli":      {source: DefaultArtifactsConfig, refused: "one container"},
		"hello-maven":       {source: DefaultArtifactsConfig, refused: "one container"},
		"hello-npm-package": {source: DefaultArtifactsConfig, refused: "one container"},
	} {
		t.Run(demo, func(t *testing.T) {
			demo, _, _ := strings.Cut(demo, "/")
			consumer := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers", demo, "consumer")

			source, err := ServiceSnapshotSource(consumer)
			if err != nil || source != want.source {
				t.Fatalf("ServiceSnapshotSource() = %q, %v, want %q", source, err, want.source)
			}

			_, plan, err := serviceSnapshotDeclarationOverlay(string(reporoot.ReadFile(t, "test/live/fixtures/consumers/"+demo+"/consumer/"+cmp.Or(want.variant, source))))
			if want.refused != "" {
				if err == nil || !strings.Contains(err.Error(), want.refused) {
					t.Fatalf("%s of %s: %v, want the development route refused for %q", source, demo, err, want.refused)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if families := serviceSnapshotFamilies(plan); !slices.Equal(families, want.families) {
				t.Fatalf("families %v, want %v", families, want.families)
			}

			if plan.Images[0].SBOM != want.sbom || !plan.Images[0].Scan {
				t.Fatalf("image SBOM %t, want %t beside the scan: %+v", plan.Images[0].SBOM, want.sbom, plan.Images[0])
			}
		})
	}
}

// The variant differs from the demo's declaration in its SBOM policy alone,
// so a snapshot of it differs from the default's in nothing but the image's
// SBOM: the producer keeps its Build layer, and the two other layers go.
func TestMavenServiceBuildSBOMDeclarationChangesThePolicyAlone(t *testing.T) {
	const consumer = "test/live/fixtures/consumers/hello-maven-service/consumer/"

	declared := serviceTestConfigPlan(t, string(reporoot.ReadFile(t, consumer+DefaultArtifactsConfig)))
	variant := serviceTestConfigPlan(t, string(reporoot.ReadFile(t, consumer+mavenServiceBuildSBOMDeclaration)))

	if !slices.Equal(variant.Artifacts.All[0].EffectiveSBOMs, []config.SBOMLayer{config.SBOMLayerBuild}) || variant.Containers.All[0].EnableAnalyzedContainerSBOM ||
		!slices.Contains(declared.Artifacts.All[0].EffectiveSBOMs, config.SBOMLayerAnalyzedContainer) || !declared.Containers.All[0].EnableAnalyzedContainerSBOM {
		t.Fatalf("SBOM layers %v beside the default's %v; want the Build layer alone and no image SBOM", variant.Artifacts.All[0].EffectiveSBOMs, declared.Artifacts.All[0].EffectiveSBOMs)
	}

	var documents [2]struct {
		Sign       map[string]any   `yaml:"sign"`
		Artifacts  []map[string]any `yaml:"artifacts"`
		Containers []map[string]any `yaml:"containers"`
	}

	for i, file := range []string{DefaultArtifactsConfig, mavenServiceBuildSBOMDeclaration} {
		if err := yaml.Unmarshal(reporoot.ReadFile(t, consumer+file), &documents[i]); err != nil {
			t.Fatal(err)
		}
	}

	if documents[1].Artifacts[0]["sboms"] != "build" {
		t.Fatalf("the variant declares sboms %v, want build", documents[1].Artifacts[0]["sboms"])
	}

	delete(documents[1].Artifacts[0], "sboms")

	if !reflect.DeepEqual(documents[0], documents[1]) {
		t.Fatalf("the variant changes more than the SBOM policy:\n%+v\n%+v", documents[1], documents[0])
	}
}

// A snapshot alone takes a declaration of its own, by a path inside the
// consumer, once.
func TestSnapshotDeclarationSettingIsExplicitAndSnapshotOnly(t *testing.T) {
	// A snapshot names the image its producers build in.
	const image = "CI_JOB_IMAGE=" + serviceTestJobImage

	for _, event := range []string{TrialPullRequest, TrialRelease, TrialLifecycle, TrialSnapshot} {
		for _, path := range []string{mavenServiceBuildSBOMDeclaration, "../outside.yml", "/absolute.yml", ".forge-ci/../artifacts.yml"} {
			settings, err := ParseTrialSettings(event, []string{image, "snapshot-declaration=" + path})

			want := event == TrialSnapshot && path == mavenServiceBuildSBOMDeclaration
			if (err == nil) != want || (want && settings.SnapshotDeclaration != path) {
				t.Fatalf("event=%s path=%s: %+v, %v", event, path, settings, err)
			}
		}
	}

	if _, err := ParseTrialSettings(TrialSnapshot, []string{image, "snapshot-declaration=a.yml", "snapshot-declaration=b.yml"}); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("duplicate declaration admitted: %v", err)
	}

	if settings, err := ParseTrialSettings(TrialSnapshot, []string{image}); err != nil || settings.SnapshotDeclaration != "" {
		t.Fatalf("a snapshot without the setting: %+v, %v", settings, err)
	}
}

// A GitLab snapshot takes its images from archives and no setting but the
// declaration it is taken from, by a path inside the consumer, once.
func TestGitLabSnapshotTakesItsDeclarationAlone(t *testing.T) {
	for settings, want := range map[string]string{
		"": "",
		"snapshot-declaration=" + mavenServiceBuildSBOMDeclaration: mavenServiceBuildSBOMDeclaration,
	} {
		if got, err := GitLabSnapshotDeclaration(settings); err != nil || got != want {
			t.Errorf("settings %q: declaration %q, %v; want %q", settings, got, err, want)
		}
	}

	for _, settings := range []string{
		"snapshot-declaration=../outside.yml", "snapshot-declaration=", "version=0.2.0", "CI_JOB_IMAGE=" + serviceTestJobImage,
		"snapshot-declaration=a.yml\nsnapshot-declaration=b.yml",
	} {
		if got, err := GitLabSnapshotDeclaration(settings); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("settings %q admitted as %q: %v", settings, got, err)
		}
	}
}

// The snapshot event's scope admits what the planner plans, every image in
// the run's one repository: the multi-image demos' declarations, each image
// narrowed to native amd64, with their producers' families. It refuses an
// image of its own repository, declared provenance, an image without amd64
// and a declaration without one.
func TestSnapshotImagesDeclarationPlansEveryImage(t *testing.T) {
	for demo, families := range map[string][]string{
		"hello-cargo-workspace": {},
		"hello-monorepo":        {snapshotimage.FamilyJava, snapshotimage.FamilyNode},
	} {
		t.Run(demo, func(t *testing.T) {
			consumer := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers", demo, "consumer")

			plan, err := snapshotImagesAdmission(consumer, "")
			if err != nil {
				t.Fatal(err)
			}

			if len(plan.Images) != 2 || len(plan.Builds) != 2 || slices.ContainsFunc(plan.Images, func(image snapshotimage.Image) bool {
				return !slices.Equal(image.Platforms, []string{"linux/amd64"}) || image.TagSuffix == ""
			}) {
				t.Fatalf("want two suffixed native amd64 images: %+v", plan)
			}

			if got := serviceSnapshotFamilies(plan); !slices.Equal(got, families) {
				t.Fatalf("families %v, want %v", got, families)
			}
		})
	}

	for name, body := range map[string]string{
		"own repository":  "containers: [{name: a, tag-suffix: -a}, {name: b, tag-suffix: -b, image-name: other/image}]",
		"no native amd64": "containers: [{name: a, tag-suffix: -a}, {name: b, tag-suffix: -b, platforms: linux/arm64}]",
	} {
		t.Run(name, func(t *testing.T) {
			if overlay, _, err := snapshotDeclarationOverlay(body, snapshotImagesScope); !errors.Is(err, errSnapshotImages) || overlay != "" {
				t.Fatalf("%q, %v; want the snapshot scope's refusal", overlay, err)
			}
		})
	}
}

// A declaration without a container is the snapshot event's too: it is staged
// as declared, and planned for its file outputs, which name its toolchain.
func TestSnapshotDeclarationWithoutContainerPlansItsFiles(t *testing.T) {
	const body = "sign: {method: sigstore}\nartifacts: [{name: app, project-type: maven}]\n"

	overlay, plan, err := snapshotDeclarationOverlay(body, snapshotImagesScope)
	if err != nil || overlay != body || len(plan.Images) != 0 || len(plan.Files) != 1 || plan.Files[0].Name != "app" {
		t.Fatalf("%q, %+v, %v; want the declaration as it is, planned for the one file output", overlay, plan.Files, err)
	}

	if families := serviceSnapshotFamilies(plan); !slices.Equal(families, []string{"java"}) {
		t.Fatalf("families %v, want the file output's java", families)
	}

	// The lifecycle judges one service, and takes no such declaration.
	if _, _, err = serviceSnapshotDeclarationOverlay(body); !errors.Is(err, errServiceDeclaration) {
		t.Fatalf("%v; want the service scope's refusal", err)
	}
}

// ToolchainFamily is the one mapping of project types to the images the
// snapshot's producers build in, on both forges.
func TestToolchainFamilyNamesEveryProducerFamily(t *testing.T) {
	families := map[string]bool{}

	for _, projectType := range []projecttype.Type{projecttype.Go, projecttype.Maven, projecttype.Gradle, projecttype.NPM, projecttype.Cargo} {
		family, err := snapshotimage.ToolchainFamily(projectType)
		if err != nil {
			t.Fatal(err)
		}

		families[family] = true
	}

	if len(families) != 4 {
		t.Fatalf("families %v, want go, java, node and rust", families)
	}

	if _, err := snapshotimage.ToolchainFamily(projecttype.Python); err == nil {
		t.Fatal("a project type without a snapshot producer has a family")
	}
}

func serviceTestConfigPlan(t *testing.T, body string) pipeline.ConfigPlan {
	t.Helper()

	cfg, err := config.Parse([]byte(body))
	if err == nil {
		err = config.Validate(cfg)
	}

	if err == nil {
		err = config.Derive(cfg)
	}

	if err != nil {
		t.Fatal(err)
	}

	return pipeline.NewConfigPlan(cfg)
}
