// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/version"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

const serviceTestJobImage = "registry.invalid/toolchain@sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestSnapshotEventDoesNotProbeSingleImageReadiness(t *testing.T) {
	recipe, _, source, _ := stageTestDemo(t, "hello-cargo-container")

	for _, snapshot := range []bool{false, true} {
		config := ServiceDevelopmentConfig{
			RecipeDirectory: recipe, JobImage: serviceTestJobImage,
			Branch: "development", ReadinessURL: "http://127.0.0.1:8080/health", Images: snapshot,
		}
		if snapshot {
			config.ReadinessURL = ""
			// No service fixture is read or executed for the snapshot event.
			config.Fixture = filepath.Join(t.TempDir(), "does-not-exist.sh")
		}

		fixture, err := serviceDevelopmentFixture(t.Context(), source, config)
		if err != nil {
			t.Fatal(err)
		}

		workflow, err := githubWorkflow(t.Context(), fixture, forgejoDevelopmentWorkflow)
		if err != nil {
			t.Fatal(err)
		}

		var document struct {
			Jobs map[string]yaml.Node `yaml:"jobs"`
		}
		if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
			t.Fatal(err)
		}

		_, readiness := document.Jobs["readiness"]

		_, builds := document.Jobs["snapshot"]
		if readiness == snapshot || !builds {
			t.Fatalf("snapshot event %t: readiness %t, snapshot job %t", snapshot, readiness, builds)
		}
	}
}

// Exercise variations entirely with local Git objects: no toolchain, API or
// container execution. The admitted objects and every non-CI blob must survive.
func TestServiceDevelopmentPreservesSource(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root, git := stageTestRepo(t, format)
			for path, body := range map[string]string{
				".forge-ci/artifacts.yml": "containers:\n  - name: service\n    container-file: Containerfile\n    context: .\n    platforms: linux/amd64\n",
				"Containerfile":           "FROM example.invalid/rust AS builder\nCOPY . /src\nRUN cd /src && cargo build\nFROM example.invalid/runtime\n",
				"src/main.rs":             "// original business code\n",
				"development/checks.toml": "[rules]\n",
			} {
				stageTestWrite(t, root, path, body, 0644)
			}

			stageTestCommit(t, git, root)

			source, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			admitted := source.Commit

			recipe := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-cargo-container/consumer")

			serviceConfig := ServiceDevelopmentConfig{RecipeDirectory: recipe, JobImage: serviceTestJobImage, Branch: "feature/other-service", ReadinessURL: "http://127.0.0.1:9090/ready"}

			fixture, err := serviceDevelopmentFixture(t.Context(), source, serviceConfig)
			if err != nil {
				t.Fatal(err)
			}

			diff, err := git.run(t.Context(), fixture.stage, "diff-tree", "--no-commit-id", "--name-only", "-r", admitted, fixture.Commit)
			if err != nil {
				t.Fatal(err)
			}

			for path := range strings.SplitSeq(diff, "\n") {
				if !strings.HasPrefix(path, ".forge-ci/") && path != forgejoDevelopmentWorkflow {
					t.Fatalf("changed source blob: %s", path)
				}
			}

			if err = git.requireClean(t.Context(), root, "."); err != nil {
				t.Fatal(err)
			}

			workflow, err := githubWorkflow(t.Context(), fixture, forgejoDevelopmentWorkflow)
			if err != nil {
				t.Fatal(err)
			}

			var parsed map[string]any
			if err = yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
				t.Fatalf("invalid rendered workflow: %v", err)
			}

			var shellJobs struct {
				Jobs map[string]struct {
					Uses    string            `yaml:"uses"`
					With    map[string]string `yaml:"with"`
					Secrets map[string]string `yaml:"secrets"`
					Steps   []struct {
						Name string            `yaml:"name"`
						Env  map[string]string `yaml:"env"`
						Run  string            `yaml:"run"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err = yaml.Unmarshal([]byte(workflow), &shellJobs); err != nil {
				t.Fatal(err)
			}

			// The snapshot builds; the harness adds readiness alone. Unsigned
			// is the fixture's policy mode, not proof that a remote registry
			// contains no signatures or attestations.
			if len(shellJobs.Jobs) != 2 || !strings.Contains(shellJobs.Jobs["snapshot"].Uses, "/snapshot-images.yml@") || len(shellJobs.Jobs["readiness"].Steps) == 0 {
				t.Fatal("the service development workflow has the snapshot and readiness roles alone")
			}

			// Which image builds a producer is the repository's variable, as
			// the snapshot-images workflow reads it; the workflow names no
			// family and no image.
			for input := range shellJobs.Jobs["snapshot"].With {
				if strings.HasPrefix(input, "artifact-job-image-") {
					t.Fatalf("the workflow binds a producer image itself: %s", input)
				}
			}

			if strings.Contains(workflow, serviceTestJobImage) || strings.Contains(workflow, "context-artifact:") {
				t.Fatal("the workflow names the job image or overrides the snapshot's context")
			}

			for name, job := range shellJobs.Jobs {
				if name == "snapshot" {
					if len(job.Secrets) != 1 || job.Secrets["SNAPSHOT_REGISTRY_PASSWORD"] != "${{ secrets.DEVELOPMENT_REGISTRY_TOKEN }}" {
						t.Fatal("snapshot must receive only the development registry credential")
					}
				} else if len(job.Secrets) != 0 {
					t.Fatalf("unexpected secret forwarding in %s", name)
				}
			}

			if strings.Count(workflow, "secrets.") != 2 || strings.Count(workflow, "${{ secrets.DEVELOPMENT_REGISTRY_TOKEN }}") != 2 {
				t.Fatal("the workflow references non-development secrets")
			}

			for name, job := range shellJobs.Jobs {
				for _, step := range job.Steps {
					command := exec.CommandContext(t.Context(), "bash", "-n")

					command.Stdin = strings.NewReader(step.Run)
					if out, err := command.CombinedOutput(); err != nil {
						t.Fatalf("invalid %s shell: %v: %s", name, err, out)
					}
				}
			}

			readiness := shellJobs.Jobs["readiness"].Steps
			stepNames := []string{}
			stepEnv := map[string]map[string]string{}
			stepRun := map[string]string{}

			for _, step := range readiness {
				if step.Name != "" {
					stepNames = append(stepNames, step.Name)
					stepEnv[step.Name], stepRun[step.Name] = step.Env, step.Run
				}
			}

			requireReadinessIsolatesTheCredential(t, stepNames, stepEnv, stepRun)

			decl, err := githubWorkflow(t.Context(), fixture, ServiceSnapshotDeclarationFile)
			if err != nil {
				t.Fatal(err)
			}

			cfg, err := parseServiceDeclaration(decl)
			if err != nil {
				t.Fatal(err)
			}

			if len(cfg.Containers) != 1 || cfg.Containers[0].EnableSLSA != nil {
				t.Fatal("the snapshot declaration must keep the release's provenance as declared")
			}

			tag, err := version.ComposeSnapshotContainerTag("9.2.0", serviceConfig.Branch, fixture.Commit[:7])
			if err != nil {
				t.Fatal(err)
			}

			plan, err := snapshotimage.New(pipeline.NewConfigPlan(cfg), snapshotimage.Input{SourceSHA: fixture.Commit, SourceBranch: serviceConfig.Branch, Tag: tag, ImageRepository: "registry.invalid/owner/service", Publish: true})
			if err != nil || len(plan.Artifacts) != 0 || len(plan.Builds) != 1 || plan.Builds[0].ContainerFile != "Containerfile" {
				t.Fatalf("invalid container-first declaration or unwanted duplicate producer: %+v %v", plan, err)
			}

			if variables := ServiceSnapshotJobImageVariables(t.Context(), t, fixture, map[string]string{lifecycleJobImage: serviceTestJobImage}); len(variables) != 0 {
				t.Fatalf("a container-first service has no producer image variable, got %v", variables)
			}

			for _, alter := range []func(*ServiceDevelopmentConfig){
				func(c *ServiceDevelopmentConfig) { c.Branch = "main" },
				func(c *ServiceDevelopmentConfig) { c.ReadinessURL = "http://external.invalid:9090/ready" },
				func(c *ServiceDevelopmentConfig) { c.JobImage = "toolchain:latest" },
				func(c *ServiceDevelopmentConfig) { c.Fixture = "relative/fixture.sh" },
			} {
				bad := serviceConfig
				alter(&bad)

				if _, err = serviceDevelopmentFixture(t.Context(), source, bad); err == nil {
					t.Fatal("accepted invalid trial infrastructure")
				}
			}
		})
	}
}

// A private consumer's snapshot image need not be anonymously readable, so the
// readiness job pulls it with the development credential in a step of its own
// that drops the job-local auth file; the operator's fixture and the service
// under test run in later steps that hold no secret.
func requireReadinessIsolatesTheCredential(t *testing.T, names []string, env map[string]map[string]string, run map[string]string) {
	t.Helper()

	const (
		pullStep    = "Pull the immutable published digest"
		fixtureStep = "Produce the nonsecret runtime fixture at the selected source"
		startStep   = "Start the pulled digest and probe its readiness"
	)

	want := []string{"Prepare isolated runtime tools", pullStep, fixtureStep, startStep}
	if !slices.Equal(names, want) {
		t.Fatalf("readiness steps %q, want %q", names, want)
	}

	pull := run[pullStep]
	login := strings.Index(pull, `forge-ci container login --registry "${ref%@*}"`)
	pulled := strings.Index(pull, `podman pull --authfile "$auth" "$ref"`)
	drop := strings.LastIndex(pull, `rm -f -- "$auth"`)

	if login < 0 || pulled < login || drop < pulled || !strings.Contains(pull[login:pulled], "unset REGISTRY_PASSWORD") || strings.Contains(pull, "podman run ") {
		t.Fatalf("the pull step must log in to a job-local auth file, pull, and drop it, starting nothing:\n%s", pull)
	}

	if env[pullStep]["REGISTRY_PASSWORD"] != "${{ secrets.DEVELOPMENT_REGISTRY_TOKEN }}" {
		t.Fatal("the pull step must take the development credential")
	}

	for _, step := range []string{fixtureStep, startStep} {
		for name, value := range env[step] {
			if strings.Contains(value, "secrets.") {
				t.Fatalf("%s holds the secret %s", step, name)
			}
		}
	}

	if !strings.Contains(run[fixtureStep], "bash .forge-ci/service-fixture.sh") || !strings.Contains(run[startStep], "podman run -d --pull=never ") {
		t.Fatal("the fixture and the service start moved out of their steps")
	}
}

// A route whose pipeline the forge's policy owns takes the declaration alone:
// no workflow, no fixture script, and the source otherwise as admitted.
func TestServiceSnapshotDeclarationStagesOnlyTheDeclaration(t *testing.T) {
	root, git := stageTestRepo(t, "sha1")
	for path, body := range map[string]string{
		".forge-ci/artifacts.yml": "containers:\n  - name: service\n    container-file: Containerfile\n    context: .\n    platforms: linux/amd64,linux/arm64\n",
		"pom.xml":                 "<project><artifactId>service</artifactId><version>1.0.0</version></project>",
		"Containerfile":           "FROM example.invalid/java\nCOPY . /src\n",
		"src/main/App.java":       "// original business code\n",
	} {
		stageTestWrite(t, root, path, body, 0644)
	}

	stageTestCommit(t, git, root)

	source, err := openConsumer(t.Context(), git, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	staged := source.WithServiceSnapshotDeclaration(t, DefaultArtifactsConfig)

	diff, err := git.run(t.Context(), staged.stage, "diff-tree", "--no-commit-id", "--name-only", "-r", source.Commit, staged.Commit)
	if err != nil {
		t.Fatal(err)
	}

	if diff != ServiceSnapshotDeclarationFile {
		t.Fatalf("the declaration staged %q, want %s alone", diff, ServiceSnapshotDeclarationFile)
	}

	declaration, err := githubWorkflow(t.Context(), staged, ServiceSnapshotDeclarationFile)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := parseServiceDeclaration(declaration)
	if err != nil || len(cfg.Containers) != 1 || cfg.Containers[0].Platforms != "linux/amd64" || cfg.Containers[0].EnableSLSA != nil {
		t.Fatalf("declaration %+v, %v; want the one container on linux/amd64 with its provenance as declared", cfg, err)
	}

	if err = git.requireClean(t.Context(), root, "."); err != nil {
		t.Fatal(err)
	}
}

// The two demos with a development route on Forgejo, through the one
// workflow: the Maven service, artifact-first, whose producer the snapshot
// builds in the java image the repository variable names; the Cargo
// container, container-first from its release-images declaration, with no
// producer and no variable. The demos' bytes and modes stay as they are.
// The Maven service's variant declaration, which a snapshot trial selects,
// keeps the producer and plans the image without an SBOM.
func TestServiceDevelopmentPreservesDemoRoutes(t *testing.T) {
	for _, demo := range []struct {
		name, declaration, variant, producer, family string
		sbom                                         bool
	}{
		{"hello-maven-service", DefaultArtifactsConfig, "", "hello-maven-service", snapshotimage.FamilyJava, true},
		{"hello-maven-service", DefaultArtifactsConfig, mavenServiceBuildSBOMDeclaration, "hello-maven-service", snapshotimage.FamilyJava, false},
		{"hello-gradle-service", DefaultArtifactsConfig, "", "hello-gradle-service", snapshotimage.FamilyJava, true},
		{"hello-cargo-container", GitLabDedicatedReleaseImagesConfig, "", "", "", false},
	} {
		t.Run(demo.name+demo.variant, func(t *testing.T) {
			recipe, root, source, git := stageTestDemo(t, demo.name)

			declaration, err := ServiceSnapshotSource(recipe)
			if err != nil || declaration != demo.declaration {
				t.Fatalf("snapshot source %q, %v; want %s", declaration, err, demo.declaration)
			}

			fixture, err := serviceDevelopmentFixture(t.Context(), source, ServiceDevelopmentConfig{
				RecipeDirectory: recipe, Declaration: cmp.Or(demo.variant, declaration), JobImage: serviceTestJobImage,
				Branch: "development", ReadinessURL: "http://127.0.0.1:8080/health",
			})
			if err != nil {
				t.Fatal(err)
			}

			changed, err := git.run(t.Context(), fixture.stage, "diff-tree", "--no-commit-id", "--name-only", "-r", source.Commit, fixture.Commit)
			if err != nil {
				t.Fatal(err)
			}

			wantChanged := ServiceSnapshotDeclarationFile + "\n.forge-ci/service-fixture.sh\n" + forgejoDevelopmentWorkflow
			if changed != wantChanged {
				t.Fatalf("original demo bytes or modes changed: %s", changed)
			}

			if err = git.requireClean(t.Context(), root, "."); err != nil {
				t.Fatal(err)
			}

			tag, err := version.ComposeSnapshotContainerTag("0.1.0", "development", fixture.Commit[:7])
			if err != nil {
				t.Fatal(err)
			}

			plan := ServiceSnapshotPlan(t.Context(), t, fixture, snapshotimage.Input{
				SourceSHA: fixture.Commit, SourceBranch: "development", Tag: tag,
				ImageRepository: "registry.invalid/owner/service", SourceDateEpoch: "1", Publish: true,
			})

			if len(plan.Builds) != 1 || plan.Builds[0].Arch != "amd64" || plan.Images[0].Signing != "NONE" {
				t.Fatalf("one unsigned native build expected: %+v", plan)
			}

			if plan.Images[0].SBOM != demo.sbom || !plan.Images[0].Scan {
				t.Fatalf("staged declaration plans image SBOM %t, want %t beside the scan", plan.Images[0].SBOM, demo.sbom)
			}

			variables := ServiceSnapshotJobImageVariables(t.Context(), t, fixture, map[string]string{lifecycleJobImage: serviceTestJobImage})

			if demo.producer == "" {
				if len(plan.Artifacts) != 0 || len(plan.Builds[0].RequiredArtifacts) != 0 || len(variables) != 0 {
					t.Fatalf("container-first route acquired a producer: %+v %v", plan, variables)
				}

				return
			}

			if len(plan.Artifacts) != 1 || plan.Artifacts[0].Name != demo.producer || len(plan.Builds[0].RequiredArtifacts) != 1 || plan.Builds[0].RequiredArtifacts[0].Name != demo.producer {
				t.Fatalf("artifact-first route lost: %+v", plan)
			}

			want := map[string]string{"SNAPSHOT_ARTIFACT_JOB_IMAGE_" + strings.ToUpper(demo.family): serviceTestJobImage}
			if !reflect.DeepEqual(variables, want) {
				t.Fatalf("producer image variables %v, want %v", variables, want)
			}
		})
	}
}

// stageTestDemo commits a shipped demo's consumer, its bytes and modes, into
// a local test repository and opens it: the recipe directory, the
// repository, the consumer and its Git.
func stageTestDemo(t *testing.T, demo string) (string, string, Consumer, layerGit) {
	t.Helper()

	root, git := stageTestRepo(t, "sha1")
	recipe := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers", demo, "consumer")

	err := fs.WalkDir(os.DirFS(recipe), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		body, err := os.ReadFile(filepath.Join(recipe, path))
		if err != nil {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		stageTestWrite(t, root, path, string(body), info.Mode().Perm())

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	stageTestCommit(t, git, root)

	source, err := openConsumer(t.Context(), git, root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return recipe, root, source, git
}

// The snapshot event takes the Cargo workspace's two images, which the
// lifecycle's one-service scope refuses: the planner plans both, each native
// amd64 under its own tag suffix in the one repository. The harness writes
// the snapshot job alone because this is the snapshot event's scope, not a
// service inference from the image count.
func TestSnapshotEventSnapshotsEveryPlannedImage(t *testing.T) {
	recipe, _, source, _ := stageTestDemo(t, "hello-cargo-workspace")

	declaration, err := ServiceSnapshotSource(recipe)
	if err != nil {
		t.Fatal(err)
	}

	config := ServiceDevelopmentConfig{
		RecipeDirectory: recipe, Declaration: declaration, JobImage: serviceTestJobImage,
		Branch: "development", ReadinessURL: "http://127.0.0.1:8080/health",
	}

	if _, err = serviceDevelopmentFixture(t.Context(), source, config); !errors.Is(err, errServiceDeclaration) {
		t.Fatalf("the lifecycle's scope took two images: %v", err)
	}

	config.Images = true

	fixture, err := serviceDevelopmentFixture(t.Context(), source, config)
	if err != nil {
		t.Fatal(err)
	}

	workflow, err := githubWorkflow(t.Context(), fixture, forgejoDevelopmentWorkflow)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(workflow, "\n  snapshot:\n") || strings.Contains(workflow, "\n  readiness:\n") || strings.Contains(workflow, "podman") {
		t.Fatalf("a plan of two images probed a readiness:\n%s", workflow)
	}

	tag, err := version.ComposeSnapshotContainerTag("0.1.0", "development", fixture.Commit[:7])
	if err != nil {
		t.Fatal(err)
	}

	plan := ServiceSnapshotPlan(t.Context(), t, fixture, snapshotimage.Input{
		SourceSHA: fixture.Commit, SourceBranch: "development", Tag: tag,
		ImageRepository: "registry.invalid/owner/workspace", SourceDateEpoch: "1", Publish: true,
	})

	suffixes := make([]string, 0, len(plan.Images))

	for _, image := range plan.Images {
		if image.ImageRepository != "registry.invalid/owner/workspace" || !slices.Equal(image.Platforms, []string{"linux/amd64"}) {
			t.Fatalf("an image outside the run's repository or not native amd64: %+v", image)
		}

		suffixes = append(suffixes, image.TagSuffix)
	}

	if !slices.Equal(suffixes, []string{"-worker", "-keytool"}) || len(plan.Builds) != 2 {
		t.Fatalf("want the worker and keytool images, one amd64 build each, and no readiness: %+v", plan)
	}
}

// The snapshot event takes the monorepo, whose two producers are of two
// families: the node family builds in the image the trial names for it,
// CI_JOB_IMAGE_NODE, and the java family, which has none of its own, in the
// trial's job image.
func TestSnapshotEventGivesEachProducerFamilyItsImage(t *testing.T) {
	recipe, _, source, _ := stageTestDemo(t, "hello-monorepo")

	declaration, err := ServiceSnapshotSource(recipe)
	if err != nil {
		t.Fatal(err)
	}

	fixture, err := serviceDevelopmentFixture(t.Context(), source, ServiceDevelopmentConfig{
		RecipeDirectory: recipe, Declaration: declaration, JobImage: serviceTestJobImage, Branch: "development", Images: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	const nodeImage = "registry.invalid/node@sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	variables := ServiceSnapshotJobImageVariables(t.Context(), t, fixture, map[string]string{
		lifecycleJobImage: serviceTestJobImage, lifecycleJobImage + "_NODE": nodeImage,
	})

	want := map[string]string{"SNAPSHOT_ARTIFACT_JOB_IMAGE_JAVA": serviceTestJobImage, "SNAPSHOT_ARTIFACT_JOB_IMAGE_NODE": nodeImage}
	if !reflect.DeepEqual(variables, want) {
		t.Fatalf("producer image variables %v, want %v", variables, want)
	}
}

// The snapshot event takes the Go CLI, whose declaration has no container,
// for its files: the harness's workflow asks for them and passes no image
// repository, registry user or secret, and the staged declaration is the
// demo's own, planned for its one binary and no image.
func TestSnapshotEventSnapshotsTheFilesOfADeclarationWithoutContainer(t *testing.T) {
	recipe, _, source, _ := stageTestDemo(t, "hello-go-cli")

	declaration, err := ServiceSnapshotSource(recipe)
	if err != nil {
		t.Fatal(err)
	}

	fixture, err := serviceDevelopmentFixture(t.Context(), source, ServiceDevelopmentConfig{
		RecipeDirectory: recipe, Declaration: declaration, JobImage: serviceTestJobImage, Branch: "development", Images: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	workflow, err := githubWorkflow(t.Context(), fixture, forgejoDevelopmentWorkflow)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(workflow, "publish-files: true\n") || strings.Contains(workflow, "\n  readiness:\n") {
		t.Fatalf("the workflow does not ask for the files alone:\n%s", workflow)
	}

	for _, registry := range []string{"image-repository", "registry-user", "secrets", "SNAPSHOT_REGISTRY_PASSWORD", "DEVELOPMENT_REGISTRY"} {
		if strings.Contains(workflow, registry) {
			t.Fatalf("a snapshot of files names %s:\n%s", registry, workflow)
		}
	}

	if files := SnapshotFileOutputs(t.Context(), t, fixture); !slices.Equal(files, []string{"my-cli"}) {
		t.Fatalf("file outputs %v, want the demo's one binary", files)
	}

	if families := ServiceSnapshotFamilies(t.Context(), t, fixture); !slices.Equal(families, []string{"go"}) {
		t.Fatalf("families %v, want the file output's go", families)
	}
}

// A Forgejo snapshot of files from the default declaration runs the demo's
// own snapshot-files caller where the demo ships one, dispatched on the
// branch it snapshots, which the caller passes on as its branch and reads
// the default declaration; any other snapshot runs the harness's workflow on
// main, which reads the staged declaration. The oracle reads what the
// workflow reads.
func TestForgejoSnapshotOfFilesRunsTheDemosOwnCaller(t *testing.T) {
	consumers := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers")
	cli := demoFiles(t, filepath.Join(consumers, "hello-go-cli/consumer", consumerWorkflows))
	npm := demoFiles(t, filepath.Join(consumers, "hello-npm-package/consumer", consumerWorkflows))

	caller := cli[ForgejoSnapshotFilesWorkflow]
	if !strings.Contains(caller, "branch: ${{ forgejo.ref_name }}\n") || strings.Contains(caller, "artifacts-config:") {
		t.Fatalf("the Go CLI's %s does not snapshot its dispatched branch from the default declaration:\n%s", ForgejoSnapshotFilesWorkflow, caller)
	}

	for name, c := range map[string]struct {
		workflows            map[string]string
		files                bool
		declaration          string
		workflow, ref, reads string
	}{
		"the Go CLI's files":             {cli, true, DefaultArtifactsConfig, ForgejoSnapshotFilesWorkflow, "development", DefaultArtifactsConfig},
		"the Go CLI from a variant":      {cli, true, ".forge-ci/variant.yml", ServiceDevelopmentWorkflow, defaultBranch, ServiceSnapshotDeclarationFile},
		"the Go CLI's images":            {cli, false, DefaultArtifactsConfig, ServiceDevelopmentWorkflow, defaultBranch, ServiceSnapshotDeclarationFile},
		"the npm package with no caller": {npm, true, DefaultArtifactsConfig, ServiceDevelopmentWorkflow, defaultBranch, ServiceSnapshotDeclarationFile},
	} {
		if workflow, ref, reads := ForgejoSnapshotRoute(c.workflows, c.files, c.declaration, "development"); workflow != c.workflow || ref != c.ref || reads != c.reads {
			t.Errorf("%s: %s on %s reading %s, want %s on %s reading %s", name, workflow, ref, reads, c.workflow, c.ref, c.reads)
		}
	}
}
