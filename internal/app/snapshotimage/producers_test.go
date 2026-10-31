// SPDX-FileCopyrightText: The Forge CI Authors
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

func npmProducerPlan(t *testing.T) snapshotimage.Plan {
	t.Helper()
	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("web", 0o700))
	require.NoError(t, os.WriteFile("web/package.json", []byte(`{"name":"snapshot-fixture","version":"1.2.3","files":["dist"],"scripts":{"test":"node test.cjs","build":"node build.cjs"}}`), 0o600))
	in := input(t, "artifacts:\n  - name: web\n    project-type: npm\n    working-directory: web\n    sboms: none\ncontainers:\n  - name: web\n    context: web\n    from: [web]\n    platforms: linux/amd64,linux/arm64\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.NoError(t, appsnapshot.WriteJSON("snapshot-plan.json", p))
	return p
}

func TestNPMProducerSafePreparationAndGather(t *testing.T) {
	p := npmProducerPlan(t)
	require.Len(t, p.Artifacts, 1)
	require.Equal(t, "web/.forge-ci/snapshot-inputs/web", p.Builds[0].RequiredArtifacts[0].Destination)
	raw := filepath.Join(snapshotimage.DefaultDownloadDir, "web")
	require.NoError(t, os.MkdirAll(raw, 0o700))
	body, err := os.ReadFile("web/package.json")
	require.NoError(t, err)
	tarball(t, filepath.Join(raw, "fixture.tgz"), string(body), "")
	require.NoError(t, appsnapshot.PrepareInputs(t.Context(), &git{head: p.SourceSHA}, p, snapshotimage.DefaultDownloadDir, snapshotimage.DefaultProducerDir))
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "context-bundle"))
	packageDir := "web/.forge-ci/snapshot-inputs/web"
	require.FileExists(t, filepath.Join(packageDir, "dist/built.js"))
	require.FileExists(t, filepath.Join(packageDir, "package.json"))
	require.NoFileExists(t, filepath.Join(packageDir, "build.cjs"))
	require.NoFileExists(t, filepath.Join(packageDir, "fixture.tgz"))
	var inventory snapshotimage.DependencyInventory
	require.NoError(t, appsnapshot.ReadJSON(snapshotimage.DefaultDependenciesFile, &inventory))
	require.Len(t, inventory.Dependencies, 2)
	require.NoError(t, appsnapshot.RestoreContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", "context-bundle"))
}

func TestNPMProducerRejectsMissingAmbiguousForeignAndUnsafeTarballs(t *testing.T) {
	for _, defect := range []string{"missing", "two tarballs", "foreign package", "traversal", "alternate root", "symlink"} {
		t.Run(defect, func(t *testing.T) {
			p := npmProducerPlan(t)
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
			err = appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, runner, p, appsnapshot.BuildArtifactInput{Name: "web", FromArtifact: "raw"})
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
			p := npmProducerPlan(t)
			require.NoError(t, os.Mkdir("raw", 0o700))
			body, err := os.ReadFile("web/package.json")
			require.NoError(t, err)
			tarball(t, "raw/a.tgz", string(body), "")
			require.NoError(t, appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, nil, p, appsnapshot.BuildArtifactInput{Name: "web", FromArtifact: "raw"}))
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
			err = appsnapshot.GatherContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle")
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
			p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
			require.NoError(t, err)
			sentinel := errors.New("runner reached")
			runner := &artifactRunner{run: func(request appsnapshot.ArtifactBuildRequest) error { return sentinel }}
			err = appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, runner, p, appsnapshot.BuildArtifactInput{Name: "producer", Environment: []string{"PATH=/tools", "REGISTRY_PASSWORD=secret", "GITHUB_TOKEN=secret", "COSIGN_KEY=secret", "SKIP_TESTS=false", "PLATFORMS=windows/amd64", "VERSION=event", "SOURCE_DATE_EPOCH=999", "DEPENDENCY_TOKEN=secret"}})
			require.ErrorIs(t, err, sentinel)
			require.Len(t, runner.requests, 1)
			request := runner.requests[0]
			require.Equal(t, []string{"build", kind, "run"}, request.Args[:3])
			require.Equal(t, "project", filepath.Base(request.Dir))
			joined := strings.Join(request.Env, "\n")
			require.NotContains(t, joined, "secret")
			require.NotContains(t, request.Env, "SOURCE_DATE_EPOCH=999")
			require.Contains(t, joined, "SOURCE_DATE_EPOCH=1")
			require.Contains(t, joined, "FORGE_CI_COMMIT="+p.SourceSHA)
			switch kind {
			case "go":
				require.Contains(t, request.Args, "--skip-tests=true")
				require.Contains(t, request.Args, "custom")
				require.Contains(t, request.Args, "-X main.extra=preserved")
				require.Contains(t, request.Args, "./cmd/app")
				require.Contains(t, request.Args, p.ArtifactVersion())
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
	p := npmProducerPlan(t)
	require.NoError(t, os.WriteFile("web/package-lock.json", []byte(`{"name":"snapshot-fixture","version":"1.2.3","lockfileVersion":3,"requires":true,"packages":{"":{"name":"snapshot-fixture","version":"1.2.3"}}}`), 0o600))
	require.NoError(t, os.WriteFile("web/build.cjs", []byte(`const fs=require('fs');fs.mkdirSync('dist',{recursive:true});fs.writeFileSync('dist/actual.js','real built bytes');`), 0o600))
	require.NoError(t, os.WriteFile("web/test.cjs", []byte(`require('fs').writeFileSync('test-ran','passed');`), 0o600))
	runner := &artifactRunner{run: func(request appsnapshot.ArtifactBuildRequest) error {
		env := append(request.Env, "npm_config_audit=false", "npm_config_fund=false", "npm_config_update_notifier=false")
		var diagnostic evidence.Diagnostic
		err := appbuild.NPMReleaseBuild(t.Context(), discardSummary{}, &npm.Adapter{Bin: npmBin, Env: env}, nil, output.Annotator{}, io.Discard, &diagnostic, appbuild.NPMReleaseBuildInput{ReleaseBuildOptions: appbuild.ReleaseBuildOptions{Dir: request.Dir}})
		if err != nil {
			t.Logf("npm producer stderr: %s", diagnostic.Text(env))
		}
		return err
	}}
	require.NoError(t, appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, runner, p, appsnapshot.BuildArtifactInput{Name: "web", Environment: []string{"PATH=" + filepath.Dir(npmBin) + ":/usr/bin:/bin"}}))
	require.FileExists(t, "web/test-ran")
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "real-bundle"))
	body, err := os.ReadFile("web/.forge-ci/snapshot-inputs/web/dist/actual.js")
	require.NoError(t, err)
	require.Equal(t, "real built bytes", string(body))
	require.NoFileExists(t, "web/.forge-ci/snapshot-inputs/web/test.cjs")
	sink := fakeoutputsink.New(t)
	require.NoError(t, appsnapshot.EmitPlan(t.Context(), sink, p))
	require.Contains(t, sink.Single("artifacts"), `"project_type":"npm"`)
}

func TestGatherEmptyProducerSetBuildsNoSource(t *testing.T) {
	t.Chdir(t.TempDir())
	in := input(t, "containers:\n  - name: image\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.Empty(t, p.Artifacts)
	require.NoError(t, appsnapshot.WriteJSON("snapshot-plan.json", p))
	require.NoError(t, appsnapshot.PrepareInputs(t.Context(), &git{head: p.SourceSHA}, p, snapshotimage.DefaultDownloadDir, snapshotimage.DefaultProducerDir))
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "empty-bundle"))
	require.FileExists(t, snapshotimage.DefaultDependenciesFile)
}

func TestAutomaticGoAndMavenMatrixAndCompleteProducerBarrier(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.Mkdir("go", 0o700))
	require.NoError(t, os.Mkdir("java", 0o700))
	in := input(t, "artifacts:\n  - name: go\n    project-type: go\n    working-directory: go\n    sboms: none\n  - name: java\n    project-type: maven\n    working-directory: java\n    sboms: none\n  - name: unused\n    project-type: npm\n    sboms: none\ncontainers:\n  - name: go\n    image-name: team/go\n    context: go\n    from: [go]\n    platforms: linux/amd64,linux/arm64\n  - name: java\n    image-name: team/java\n    context: java\n    from: [java]\n    platforms: linux/amd64\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.Len(t, p.Artifacts, 2)
	require.NoError(t, appsnapshot.WriteJSON("snapshot-plan.json", p))
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
	for _, a := range p.Artifacts {
		require.NoError(t, appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, runner, p, appsnapshot.BuildArtifactInput{Name: a.Name}))
	}
	require.Len(t, runner.requests, 2)
	receiptPath := "_snapshot-artifacts/java/snapshot-artifact.json"
	receipt, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(receiptPath))
	require.NoError(t, os.WriteFile("go/dist/linux-amd64/go-linux-amd64", []byte("previous checkout"), 0o700))
	err = appsnapshot.GatherContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle")
	require.Error(t, err)
	body, err := os.ReadFile("go/dist/linux-amd64/go-linux-amd64")
	require.NoError(t, err)
	require.Equal(t, "previous checkout", string(body))
	require.NoFileExists(t, snapshotimage.DefaultDependenciesFile)
	require.NoError(t, os.WriteFile(receiptPath, receipt, 0o600))
	require.NoError(t, appsnapshot.GatherContext(t.Context(), &git{head: p.SourceSHA}, p, "snapshot-plan.json", snapshotimage.DefaultProducerDir, "bundle"))
	body, err = os.ReadFile("go/dist/linux-amd64/go-linux-amd64")
	require.NoError(t, err)
	require.Equal(t, "producer amd64", string(body))
	require.Len(t, runner.requests, 2, "gather must never rebuild")
}

func TestProducerDataCannotOmitOrRebindDependencies(t *testing.T) {
	for _, defect := range []string{"missing matrix", "duplicate producer", "wrong upload", "foreign config", "missing platform", "bad epoch", "wrong destination", "extra producer", "inconsistent SBOM policy"} {
		t.Run(defect, func(t *testing.T) {
			p, _ := dependencyFixture(t)
			switch defect {
			case "missing matrix":
				p.Artifacts = nil
			case "duplicate producer":
				p.Artifacts = append(p.Artifacts, p.Artifacts[0])
			case "wrong upload":
				p.Artifacts[0].BuildArtifactName = "foreign-upload"
			case "foreign config":
				p.Artifacts[0].NPM = &config.NPMConfig{}
			case "missing platform":
				p.Artifacts[0].Go = &config.GoConfig{Platforms: "windows/amd64"}
			case "bad epoch":
				p.SourceDateEpoch = "event-date"
			case "wrong destination":
				p.Builds[0].RequiredArtifacts[0].Destination = "other/dist"
			case "extra producer":
				copy := p.Artifacts[0]
				copy.Name = "unneeded"
				copy.BuildArtifactName = "unneeded-go-build-artifacts"
				p.Artifacts = append(p.Artifacts, copy)
			case "inconsistent SBOM policy":
				p.Artifacts[0].EffectiveSBOMs = nil
			}
			runner := &artifactRunner{}
			err := appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, runner, p, appsnapshot.BuildArtifactInput{Name: "server"})
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
			p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
			require.NoError(t, err)
			sentinel := errors.New("request inspected")
			runner := &artifactRunner{run: func(request appsnapshot.ArtifactBuildRequest) error { return sentinel }}
			err = appsnapshot.BuildArtifact(t.Context(), &git{head: p.SourceSHA}, runner, p, appsnapshot.BuildArtifactInput{Name: "source"})
			require.ErrorIs(t, err, sentinel)
			expected := "--build-sbom=true"
			if policy == "none" {
				expected = "--build-sbom=false"
			}
			require.Contains(t, runner.requests[0].Args, expected)
		})
	}
}
