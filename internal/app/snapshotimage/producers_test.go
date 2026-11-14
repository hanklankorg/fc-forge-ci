// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/npm"
	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/stretchr/testify/require"
)

type artifactRunner struct {
	requests []appsnapshot.ArtifactBuildRequest
	run      func(appsnapshot.ArtifactBuildRequest) error
}
type discardSummary struct{}

func (discardSummary) Append(context.Context, string) error { return nil }
func (r *artifactRunner) Run(_ context.Context, in appsnapshot.ArtifactBuildRequest) error {
	r.requests = append(r.requests, in)
	if r.run != nil {
		return r.run(in)
	}

	return nil
}

func tarball(t *testing.T, path, manifest, extra string) {
	t.Helper()

	file, err := os.Create(path)
	require.NoError(t, err)

	gz := gzip.NewWriter(file)

	writer := tar.NewWriter(gz)
	for _, entry := range []struct{ name, body string }{{"package/package.json", manifest}, {"package/dist/built.js", "module.exports = 42;"}} {
		require.NoError(t, writer.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}))
		_, err = writer.Write([]byte(entry.body))
		require.NoError(t, err)
	}

	if extra != "" {
		require.NoError(t, writer.WriteHeader(&tar.Header{Name: extra, Mode: 0o644, Size: 4, Typeflag: tar.TypeReg}))
		_, err = writer.Write([]byte("evil"))
		require.NoError(t, err)
	}

	require.NoError(t, writer.Close())
	require.NoError(t, gz.Close())
	require.NoError(t, file.Close())
}

// npmBuildInput builds the npm fixture's amd64 image on an amd64 host.
var npmBuildInput = appsnapshot.BuildInput{Name: "web", Arch: "amd64", HostArch: "amd64", MetadataDir: "metadata", ServerURL: "https://forge.example", Repository: "team/app"}

func npmProducerPlan(t *testing.T) snapshotimage.Plan {
	t.Helper()
	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("web", 0o700))
	require.NoError(t, os.WriteFile("web/package.json", []byte(`{"name":"snapshot-fixture","version":"1.2.3","files":["dist"],"scripts":{"test":"node test.cjs","build":"node build.cjs"}}`), 0o600))
	in := input(t, "artifacts:\n  - name: web\n    project-type: npm\n    working-directory: web\n    sboms: none\ncontainers:\n  - name: web\n    context: web\n    from: [web]\n    platforms: linux/amd64,linux/arm64\n")
	plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.NoError(t, appsnapshot.WriteJSON("snapshot-plan.json", plan))

	return plan
}

func TestNPMProducerSafePreparationAndGather(t *testing.T) {
	plan := npmProducerPlan(t)
	require.Len(t, plan.Artifacts, 1)
	require.Equal(t, "web/.forge-ci/snapshot-inputs/web", plan.Builds[0].RequiredArtifacts[0].Destination)

	raw := filepath.Join(snapshotimage.DefaultDownloadDir, "web")
	require.NoError(t, os.MkdirAll(raw, 0o700))

	source, err := os.ReadFile("web/package.json")
	require.NoError(t, err)
	// The packed manifest may differ from the committed one beyond its identity.
	packed := strings.Replace(string(source), `"files"`, `"main":"dist/built.js","files"`, 1)
	tarball(t, filepath.Join(raw, "fixture.tgz"), packed, "")
	require.NoError(t, appsnapshot.PrepareInputs(t.Context(), &git{head: plan.SourceSHA}, plan, snapshotimage.DefaultDownloadDir, snapshotimage.DefaultProducerDir))
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: plan.SourceSHA}, plan, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "context-bundle"))

	packageDir := "web/.forge-ci/snapshot-inputs/web"
	require.FileExists(t, filepath.Join(packageDir, "dist/built.js"))
	require.FileExists(t, filepath.Join(packageDir, "package.json"))
	require.NoFileExists(t, filepath.Join(packageDir, "build.cjs"))
	require.NoFileExists(t, filepath.Join(packageDir, "fixture.tgz"))
	// Gathering verifies the package; the checkout's own files are untouched
	// until a build lays the package over its context.
	require.NoFileExists(t, "web/dist/built.js")

	var inventory snapshotimage.DependencyInventory
	require.NoError(t, appsnapshot.ReadJSON(snapshotimage.DefaultDependenciesFile, &inventory))
	require.Len(t, inventory.Dependencies, 2)
	require.NoError(t, appsnapshot.RestoreContext(t.Context(), &git{head: plan.SourceSHA}, plan, "snapshot-plan.json", "context-bundle"))

	// The build lays the verified package over the context root, where the
	// Containerfile copies it as a release image build does: COPY dist/ ...
	builder := &countingBuilder{}
	err = appsnapshot.Build(t.Context(), &git{head: plan.SourceSHA}, builder, ociregistry.NewAnonymous(), io.Discard, output.Annotator{}, plan, npmBuildInput)
	require.ErrorIs(t, err, errBuilderReached)
	require.Equal(t, 1, builder.calls)

	built, err := os.ReadFile("web/dist/built.js")
	require.NoError(t, err)
	require.Equal(t, "module.exports = 42;", string(built))

	manifest, err := os.ReadFile("web/package.json")
	require.NoError(t, err)
	require.Equal(t, packed, string(manifest), "the packed manifest replaces the committed one, as the release image build's unpacked package does")
	require.FileExists(t, filepath.Join(packageDir, "package.json"), "the verified inputs stay where the inventory records them")
}

func TestNPMLayoutRefusesSymlinkedTargetsBeforeTheBuilder(t *testing.T) {
	plan := npmProducerPlan(t)
	require.NoError(t, os.Mkdir("raw", 0o700))

	body, err := os.ReadFile("web/package.json")
	require.NoError(t, err)
	tarball(t, "raw/a.tgz", string(body), "")
	require.NoError(t, appsnapshot.BuildArtifact(t.Context(), &git{head: plan.SourceSHA}, nil, plan, appsnapshot.BuildArtifactInput{Name: "web", FromArtifact: "raw"}))
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: plan.SourceSHA}, plan, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle"))

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "built.js"), []byte("outside unchanged"), 0o600))
	require.NoError(t, os.Symlink(outside, "web/dist"))

	builder := &countingBuilder{}
	err = appsnapshot.Build(t.Context(), &git{head: plan.SourceSHA}, builder, ociregistry.NewAnonymous(), io.Discard, output.Annotator{}, plan, npmBuildInput)
	require.Error(t, err)
	require.Zero(t, builder.calls)

	canary, err := os.ReadFile(filepath.Join(outside, "built.js"))
	require.NoError(t, err)
	require.Equal(t, "outside unchanged", string(canary))

	manifest, err := os.ReadFile("web/package.json")
	require.NoError(t, err)
	require.Equal(t, body, manifest, "a refused layout writes nothing to the checkout")
}

// npmBesideGoFixture plans one image at the checkout root from a Go binary
// and an npm package, lays both verified inputs by hand where the plan
// records them, the npm package also carrying the named file, and records the
// inventory as the context producer does.
func npmBesideGoFixture(t *testing.T, carried string) snapshotimage.Plan {
	t.Helper()
	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("web", 0o700))
	require.NoError(t, os.WriteFile("web/package.json", []byte(`{"name":"web","version":"1.2.3"}`), 0o600))
	in := input(t, "artifacts:\n  - name: server\n    project-type: go\n  - name: web\n    project-type: npm\n    working-directory: web\n    sboms: none\ncontainers:\n  - name: app\n    from: [server, web]\n    platforms: linux/amd64\n")
	plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("Containerfile", []byte("FROM scratch\nCOPY dist /dist\n"), 0o600))
	require.NoError(t, os.MkdirAll("dist/linux-amd64", 0o700))
	require.NoError(t, os.WriteFile("dist/linux-amd64/server-linux-amd64", []byte("producer-built bytes"), 0o700))

	verified := ".forge-ci/snapshot-inputs/web"
	for path, body := range map[string]string{"package.json": `{"name":"web","version":"1.2.3"}`, carried: "evil"} {
		require.NoError(t, os.MkdirAll(filepath.Join(verified, filepath.Dir(path)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(verified, path), []byte(body), 0o600))
	}

	require.NoError(t, appsnapshot.RecordContext(t.Context(), &git{head: plan.SourceSHA}, plan, snapshotimage.DefaultDependenciesFile))

	return plan
}

func TestNPMLayoutRefusesReplacingBuildInputsBeforeTheBuilder(t *testing.T) {
	inputs := []string{"Containerfile", "dist/linux-amd64/server-linux-amd64", ".forge-ci/snapshot-inputs/web/package.json"}
	for _, carried := range inputs {
		t.Run(carried, func(t *testing.T) {
			plan := npmBesideGoFixture(t, carried)

			before := map[string][]byte{}

			for _, path := range inputs {
				body, err := os.ReadFile(path)
				require.NoError(t, err)

				before[path] = body
			}

			builder := &countingBuilder{}
			err := appsnapshot.Build(t.Context(), &git{head: plan.SourceSHA}, builder, ociregistry.NewAnonymous(), io.Discard, output.Annotator{}, plan, appsnapshot.BuildInput{Name: "app", Arch: "amd64", HostArch: "amd64", MetadataDir: "metadata", ServerURL: "https://forge.example", Repository: "team/app"})
			require.ErrorContains(t, err, "would replace the build's Containerfile or a dependency input: "+carried)
			require.Zero(t, builder.calls)

			for path, body := range before {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, body, after, "%s keeps its verified bytes", path)
			}

			require.NoFileExists(t, "package.json", "a refused layout lays none of the package's files")
		})
	}
}

func TestPlanRefusesTwoNPMPackagesInOneImage(t *testing.T) {
	t.Chdir(t.TempDir())

	for _, dir := range []string{"web", "api"} {
		require.NoError(t, os.Mkdir(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"`+dir+`","version":"1.2.3"}`), 0o600))
	}

	artifacts := "artifacts:\n  - name: web\n    project-type: npm\n    working-directory: web\n    sboms: none\n  - name: api\n    project-type: npm\n    working-directory: api\n    sboms: none\n"
	_, err := appsnapshot.Plan(t.Context(), &git{head: strings.Repeat("a", 40)}, input(t, artifacts+"containers:\n  - name: image\n    from: [web, api]\n    platforms: linux/amd64\n"))
	require.ErrorContains(t, err, "one npm package")

	// A frozen plan read back admits the same rule.
	plan, err := appsnapshot.Plan(t.Context(), &git{head: strings.Repeat("a", 40)}, input(t, artifacts+"containers:\n  - name: image\n    from: [web]\n    platforms: linux/amd64\n  - name: other\n    image-name: team/other\n    from: [api]\n    platforms: linux/amd64\n"))
	require.NoError(t, err)
	require.NoError(t, plan.Validate())
	plan.Builds[0].RequiredArtifacts = append(plan.Builds[0].RequiredArtifacts, plan.Builds[1].RequiredArtifacts[0])
	require.ErrorContains(t, plan.Validate(), "one npm package")
}

func TestNPMProducerRejectsMissingAmbiguousForeignAndUnsafeTarballs(t *testing.T) {
	for _, defect := range []string{"missing", "two tarballs", "foreign package", "traversal", "alternate root", "symlink"} {
		t.Run(defect, func(t *testing.T) {
			plan := npmProducerPlan(t)
			require.NoError(t, os.Mkdir("raw", 0o700))

			body, err := os.ReadFile("web/package.json")
			require.NoError(t, err)

			extra := ""
			if defect == "traversal" {
				extra = "package/../outside"
			}

			if defect == "alternate root" {
				extra = "other/file"
			}

			if defect == "foreign package" {
				body = []byte(`{"name":"foreign","version":"1.2.3"}`)
			}

			if defect != "missing" {
				tarball(t, "raw/a.tgz", string(body), extra)
			}

			if defect == "two tarballs" {
				tarball(t, "raw/b.tgz", string(body), "")
			}

			if defect == "symlink" {
				require.NoError(t, os.Rename("raw/a.tgz", "raw/real"))
				require.NoError(t, os.Symlink("real", "raw/a.tgz"))
			}

			runner := &artifactRunner{}
			err = appsnapshot.BuildArtifact(t.Context(), &git{head: plan.SourceSHA}, runner, plan, appsnapshot.BuildArtifactInput{Name: "web", FromArtifact: "raw"})
			require.Error(t, err)
			require.Empty(t, runner.requests)
			require.NoDirExists(t, "_snapshot-artifacts/web")
			require.NoFileExists(t, "outside")
		})
	}
}

func TestGatherRejectsMissingForeignAndSubstitutedProducersBeforeCheckoutWrites(t *testing.T) {
	for _, defect := range []string{"missing producer", "extra producer", "foreign source", "wrong artifact", "wrong platform", "wrong plan", "substitution", "extra file", "destination symlink"} {
		t.Run(defect, func(t *testing.T) {
			plan := npmProducerPlan(t)
			require.NoError(t, os.Mkdir("raw", 0o700))

			body, err := os.ReadFile("web/package.json")
			require.NoError(t, err)
			tarball(t, "raw/a.tgz", string(body), "")
			require.NoError(t, appsnapshot.BuildArtifact(t.Context(), &git{head: plan.SourceSHA}, nil, plan, appsnapshot.BuildArtifactInput{Name: "web", FromArtifact: "raw"}))

			manifestPath := "_snapshot-artifacts/web/snapshot-artifact.json"

			var receipt snapshotimage.ArtifactReceipt
			require.NoError(t, appsnapshot.ReadJSON(manifestPath, &receipt))

			switch defect {
			case "missing producer":
				require.NoError(t, os.RemoveAll("_snapshot-artifacts/web"))
			case "extra producer":
				require.NoError(t, os.Mkdir("_snapshot-artifacts/extra", 0o700))
			case "foreign source":
				receipt.SourceSHA = strings.Repeat("b", 40)
			case "wrong artifact":
				receipt.Artifact.Name = "foreign"
			case "wrong platform":
				receipt.Platforms = []string{"linux/amd64"}
			case "wrong plan":
				receipt.PlanSHA256 = strings.Repeat("a", 64)
			case "substitution":
				require.NoError(t, os.WriteFile("_snapshot-artifacts/web/files/web/.forge-ci/snapshot-inputs/web/dist/built.js", []byte("substitute"), 0o600))
			case "extra file":
				require.NoError(t, os.WriteFile("_snapshot-artifacts/web/extra", []byte("unplanned"), 0o600))
			case "destination symlink":
				outside := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(outside, "canary"), []byte("outside"), 0o600))
				require.NoError(t, os.Symlink(outside, "web/.forge-ci"))
			}

			if slices.Contains([]string{"foreign source", "wrong artifact", "wrong platform", "wrong plan"}, defect) {
				require.NoError(t, appsnapshot.WriteJSON(manifestPath, receipt))
			}

			err = appsnapshot.GatherContext(t.Context(), &git{head: plan.SourceSHA}, plan, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle")
			require.Error(t, err)
			require.NoFileExists(t, snapshotimage.DefaultDependenciesFile)
			require.NoDirExists(t, "bundle")

			if defect != "destination symlink" {
				require.NoDirExists(t, "web/.forge-ci")
			}
		})
	}
}

func TestFrozenBuildOptionsAndCredentialIsolation(t *testing.T) {
	for _, kind := range []string{"go", "cargo", "maven", "gradle", "npm"} {
		t.Run(kind, func(t *testing.T) {
			t.Chdir(t.TempDir())
			require.NoError(t, os.Mkdir("project", 0o700))

			configuration := ""

			switch kind {
			case "go":
				configuration = "    config:\n      main-package: ./cmd/app\n      binary-name: selected\n      build-tags: custom\n      ldflags: -X main.extra=preserved\n      skip-tests: true\n"
			case "cargo":
				configuration = "    config:\n      binary-name: selected\n      skip-tests: true\n"
			case "maven":
				configuration = "    build-type: library\n    config:\n      maven-profile: selected-profile\n"
			case "gradle":
				configuration = "    config:\n      gradle-tasks: assemble check\n"
			}

			in := input(t, "artifacts:\n  - name: producer\n    project-type: "+kind+"\n    working-directory: project\n    sboms: none\n"+configuration+"containers:\n  - name: image\n    context: project\n    from: [producer]\n    platforms: linux/amd64\n")
			plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
			require.NoError(t, err)

			sentinel := errors.New("runner reached")
			runner := &artifactRunner{run: func(request appsnapshot.ArtifactBuildRequest) error { return sentinel }}
			err = appsnapshot.BuildArtifact(t.Context(), &git{head: plan.SourceSHA}, runner, plan, appsnapshot.BuildArtifactInput{Name: "producer", Environment: []string{"PATH=/tools", "REGISTRY_PASSWORD=secret", "GITHUB_TOKEN=secret", "COSIGN_KEY=secret", "SKIP_TESTS=false", "PLATFORMS=windows/amd64", "VERSION=event", "SOURCE_DATE_EPOCH=999", "DEPENDENCY_TOKEN=secret"}})
			require.ErrorIs(t, err, sentinel)
			require.Len(t, runner.requests, 1)
			request := runner.requests[0]
			require.Equal(t, []string{"build", kind, "run"}, request.Args[:3])
			require.Equal(t, "project", filepath.Base(request.Dir))
			joined := strings.Join(request.Env, "\n")
			require.NotContains(t, joined, "secret")
			require.NotContains(t, request.Env, "SOURCE_DATE_EPOCH=999")
			require.Contains(t, joined, "SOURCE_DATE_EPOCH=1")
			require.Contains(t, joined, "FORGE_CI_COMMIT="+plan.SourceSHA)

			switch kind {
			case "go":
				require.Contains(t, request.Args, "--skip-tests=true")
				require.Contains(t, request.Args, "custom")
				require.Contains(t, request.Args, "-X main.extra=preserved")
				require.Contains(t, request.Args, "./cmd/app")
				require.Contains(t, request.Args, plan.ArtifactVersion())
			case "cargo":
				require.Contains(t, request.Args, "selected")
				require.Contains(t, request.Args, "--skip-tests=true")
			case "maven":
				require.Contains(t, request.Args, "selected-profile")
				require.Contains(t, request.Args, "lib")
			case "gradle":
				require.Contains(t, request.Args, "assemble check")
			case "npm":
				require.Contains(t, request.Args, "--skip-tests=false")
			}
		})
	}
}

func TestAutomaticNPMProducerWithRealExistingBuild(t *testing.T) {
	npmBin, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("real npm fixture requires npm")
	}

	plan := npmProducerPlan(t)
	require.NoError(t, os.WriteFile("web/package-lock.json", []byte(`{"name":"snapshot-fixture","version":"1.2.3","lockfileVersion":3,"requires":true,"packages":{"":{"name":"snapshot-fixture","version":"1.2.3"}}}`), 0o600))
	require.NoError(t, os.WriteFile("web/build.cjs", []byte(`const fs=require('fs');fs.mkdirSync('dist',{recursive:true});fs.writeFileSync('dist/actual.js','real built bytes');`), 0o600))
	require.NoError(t, os.WriteFile("web/test.cjs", []byte(`require('fs').writeFileSync('test-ran','passed');`), 0o600))

	runner := &artifactRunner{run: func(request appsnapshot.ArtifactBuildRequest) error {
		env := slices.Concat(request.Env, []string{"npm_config_audit=false", "npm_config_fund=false", "npm_config_update_notifier=false"})

		var diagnostic evidence.Diagnostic

		err := appbuild.NPMReleaseBuild(t.Context(), discardSummary{}, &npm.Adapter{Bin: npmBin, Env: env}, nil, output.Annotator{}, io.Discard, &diagnostic, appbuild.NPMReleaseBuildInput{Dir: request.Dir})
		if err != nil {
			t.Logf("npm producer stderr: %s", diagnostic.Text(env))
		}

		return err
	}}
	require.NoError(t, appsnapshot.BuildArtifact(t.Context(), &git{head: plan.SourceSHA}, runner, plan, appsnapshot.BuildArtifactInput{Name: "web", Environment: []string{"PATH=" + filepath.Dir(npmBin) + ":/usr/bin:/bin"}}))
	require.FileExists(t, "web/test-ran")
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: plan.SourceSHA}, plan, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "real-bundle"))

	body, err := os.ReadFile("web/.forge-ci/snapshot-inputs/web/dist/actual.js")
	require.NoError(t, err)
	require.Equal(t, "real built bytes", string(body))
	require.NoFileExists(t, "web/.forge-ci/snapshot-inputs/web/test.cjs")
	sink := fakeoutputsink.New(t)
	require.NoError(t, appsnapshot.EmitPlan(t.Context(), sink, plan))
	require.Contains(t, sink.Single("artifacts"), `"project_type":"npm"`)
}

func TestGatherEmptyProducerSetBuildsNoSource(t *testing.T) {
	t.Chdir(t.TempDir())
	in := input(t, "containers:\n  - name: image\n")
	plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.Empty(t, plan.Artifacts)
	require.NoError(t, appsnapshot.WriteJSON("snapshot-plan.json", plan))
	require.NoError(t, appsnapshot.PrepareInputs(t.Context(), &git{head: plan.SourceSHA}, plan, snapshotimage.DefaultDownloadDir, snapshotimage.DefaultProducerDir))
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: plan.SourceSHA}, plan, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "empty-bundle"))
	require.FileExists(t, snapshotimage.DefaultDependenciesFile)
}

func TestAutomaticGoAndMavenMatrixAndCompleteProducerBarrier(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("go", 0o700))
	require.NoError(t, os.Mkdir("java", 0o700))
	in := input(t, "artifacts:\n  - name: go\n    project-type: go\n    working-directory: go\n    sboms: none\n  - name: java\n    project-type: maven\n    working-directory: java\n    sboms: none\n  - name: unused\n    project-type: npm\n    sboms: none\ncontainers:\n  - name: go\n    image-name: team/go\n    context: go\n    from: [go]\n    platforms: linux/amd64,linux/arm64\n  - name: java\n    image-name: team/java\n    context: java\n    from: [java]\n    platforms: linux/amd64\n")
	plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.Len(t, plan.Artifacts, 2)
	require.NoError(t, appsnapshot.WriteJSON("snapshot-plan.json", plan))

	runner := &artifactRunner{run: func(request appsnapshot.ArtifactBuildRequest) error {
		if request.Args[1] == "go" {
			for _, arch := range []string{"amd64", "arm64"} {
				dir := filepath.Join(request.Dir, "dist", "linux-"+arch)
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "go-linux-"+arch), []byte("producer "+arch), 0o700))
			}
		} else {
			dir := filepath.Join(request.Dir, "target")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "app.jar"), []byte("producer JAR"), 0o600))
		}

		return nil
	}}
	for _, a := range plan.Artifacts {
		require.NoError(t, appsnapshot.BuildArtifact(t.Context(), &git{head: plan.SourceSHA}, runner, plan, appsnapshot.BuildArtifactInput{Name: a.Name}))
	}

	require.Len(t, runner.requests, 2)

	receiptPath := "_snapshot-artifacts/java/snapshot-artifact.json"
	receipt, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(receiptPath))
	require.NoError(t, os.WriteFile("go/dist/linux-amd64/go-linux-amd64", []byte("previous checkout"), 0o700))
	err = appsnapshot.GatherContext(t.Context(), &git{head: plan.SourceSHA}, plan, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle")
	require.Error(t, err)
	body, err := os.ReadFile("go/dist/linux-amd64/go-linux-amd64")
	require.NoError(t, err)
	require.Equal(t, "previous checkout", string(body))
	require.NoFileExists(t, snapshotimage.DefaultDependenciesFile)
	require.NoError(t, os.WriteFile(receiptPath, receipt, 0o600))
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: plan.SourceSHA}, plan, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle"))

	body, err = os.ReadFile("go/dist/linux-amd64/go-linux-amd64")
	require.NoError(t, err)
	require.Equal(t, "producer amd64", string(body))
	require.Len(t, runner.requests, 2, "gather must never rebuild")
}

// The hosted Maven build uploads its POM beside target/, as build-maven.yml
// lists them. The import takes the jar from there to where the Containerfile
// copies it from, and leaves the POM out of the image inputs.
func TestPrepareInputsReadsAMavenDownloadAsTheBuildUploadsIt(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("java", 0o700))
	in := input(t, "artifacts:\n  - name: java\n    project-type: maven\n    working-directory: java\n    sboms: none\ncontainers:\n  - name: java\n    context: java\n    from: [java]\n")
	plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.Equal(t, "java/target", plan.Builds[0].RequiredArtifacts[0].Destination)
	require.NoError(t, appsnapshot.WriteJSON("snapshot-plan.json", plan))

	download := filepath.Join(snapshotimage.DefaultDownloadDir, "java")
	require.NoError(t, os.MkdirAll(filepath.Join(download, "target"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(download, "pom.xml"), []byte("<project/>"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(download, "target", "app.jar"), []byte("built JAR"), 0o600))

	require.NoError(t, appsnapshot.PrepareInputs(t.Context(), &git{head: plan.SourceSHA}, plan, snapshotimage.DefaultDownloadDir, snapshotimage.DefaultProducerDir))
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: plan.SourceSHA}, plan, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle"))

	body, err := os.ReadFile("java/target/app.jar")
	require.NoError(t, err)
	require.Equal(t, "built JAR", string(body))
	require.NoFileExists(t, "java/target/pom.xml")
}

// The hosted Gradle build uploads build/libs of the project and of every
// subproject, as build-gradle-app.yml lists them, so the download keeps that
// directory. The import takes the project's jar from there to where the
// Containerfile copies it from.
func TestPrepareInputsReadsAGradleDownloadAsTheBuildUploadsIt(t *testing.T) {
	t.Chdir(t.TempDir())
	in := input(t, "artifacts:\n  - name: java\n    project-type: gradle\n    sboms: none\ncontainers:\n  - name: java\n    from: [java]\n")
	plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.Equal(t, "build/libs", plan.Builds[0].RequiredArtifacts[0].Destination)
	require.NoError(t, appsnapshot.WriteJSON("snapshot-plan.json", plan))

	download := filepath.Join(snapshotimage.DefaultDownloadDir, "java", "build", "libs")
	require.NoError(t, os.MkdirAll(download, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(download, "app.jar"), []byte("built JAR"), 0o600))

	require.NoError(t, appsnapshot.PrepareInputs(t.Context(), &git{head: plan.SourceSHA}, plan, snapshotimage.DefaultDownloadDir, snapshotimage.DefaultProducerDir))
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: plan.SourceSHA}, plan, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle"))

	body, err := os.ReadFile("build/libs/app.jar")
	require.NoError(t, err)
	require.Equal(t, "built JAR", string(body))
}

func TestProducerDataCannotOmitOrRebindDependencies(t *testing.T) {
	for _, defect := range []string{"missing matrix", "duplicate producer", "wrong upload", "foreign config", "missing platform", "bad epoch", "wrong destination", "extra producer", "inconsistent SBOM policy"} {
		t.Run(defect, func(t *testing.T) {
			plan, _ := dependencyFixture(t)

			switch defect {
			case "missing matrix":
				plan.Artifacts = nil
			case "duplicate producer":
				plan.Artifacts = append(plan.Artifacts, plan.Artifacts[0])
			case "wrong upload":
				plan.Artifacts[0].BuildArtifactName = "foreign-upload"
			case "foreign config":
				plan.Artifacts[0].NPM = &config.NPMConfig{}
			case "missing platform":
				plan.Artifacts[0].Go = &config.GoConfig{Platforms: "windows/amd64"}
			case "bad epoch":
				plan.SourceDateEpoch = "event-date"
			case "wrong destination":
				plan.Builds[0].RequiredArtifacts[0].Destination = "other/dist"
			case "extra producer":
				extra := plan.Artifacts[0]
				extra.Name = "unneeded"
				extra.BuildArtifactName = "unneeded-go-build-artifacts"
				plan.Artifacts = append(plan.Artifacts, extra)
			case "inconsistent SBOM policy":
				plan.Artifacts[0].EffectiveSBOMs = nil
			}

			runner := &artifactRunner{}
			err := appsnapshot.BuildArtifact(t.Context(), &git{head: plan.SourceSHA}, runner, plan, appsnapshot.BuildArtifactInput{Name: "server"})
			require.Error(t, err)
			require.Empty(t, runner.requests)
		})
	}
}

func TestProducerPreservesDeclaredBuildSBOMPolicy(t *testing.T) {
	for _, policy := range []string{"default", "none"} {
		t.Run(policy, func(t *testing.T) {
			t.Chdir(t.TempDir())

			extra := ""
			if policy == "none" {
				extra = "    sboms: none\n"
			}

			in := input(t, "artifacts:\n  - name: source\n    project-type: go\n"+extra+"containers:\n  - name: image\n    from: [source]\n")
			plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
			require.NoError(t, err)

			sentinel := errors.New("request inspected")
			runner := &artifactRunner{run: func(request appsnapshot.ArtifactBuildRequest) error { return sentinel }}
			err = appsnapshot.BuildArtifact(t.Context(), &git{head: plan.SourceSHA}, runner, plan, appsnapshot.BuildArtifactInput{Name: "source"})
			require.ErrorIs(t, err, sentinel)

			expected := "--build-sbom=true"
			if policy == "none" {
				expected = "--build-sbom=false"
			}

			require.Contains(t, runner.requests[0].Args, expected)

			// A job that brings the generator is told what the build is.
			enabled, err := appsnapshot.BuildSBOM(plan, "source")
			require.NoError(t, err)
			require.Equal(t, policy != "none", enabled)
		})
	}
}
