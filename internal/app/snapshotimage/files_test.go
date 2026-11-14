// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/stretchr/testify/require"
)

// filesDeclaration is a release declaration without a container: one command
// built outside an image, signed in a release.
const filesDeclaration = "sign:\n  method: sigstore\nartifacts:\n  - name: cli\n    project-type: go\n    sboms: none\n    config:\n      build-mode: artifact-first\n      platforms: linux/amd64,darwin/arm64\n"

func filesPlan(t *testing.T, declaration string) snapshotimage.Plan {
	t.Helper()
	t.Chdir(t.TempDir())

	in := input(t, declaration)
	in.ImageRepository, in.PublishFiles = "", true

	plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)

	return plan
}

// A project without a container publishes no image, and nothing at all
// unless its caller asks for the files; then every artifact built outside an
// image is planned under a name that carries the snapshot version.
func TestFilesAreKeptOnlyWhenAskedFor(t *testing.T) {
	plan := filesPlan(t, filesDeclaration)
	require.False(t, plan.Enabled)
	require.Empty(t, plan.Images)
	require.Empty(t, plan.Artifacts)
	require.Equal(t, []snapshotimage.FileOutput{{Name: "cli", ProjectType: "go", ArtifactName: "cli-1.2.3-snapshot-manual-branch-aaaaaaa"}}, plan.FileOutputs())

	sink := fakeoutputsink.New(t)
	require.NoError(t, appsnapshot.EmitPlan(t.Context(), sink, plan))
	require.Equal(t, "true", sink.Single("has-files"))
	require.Equal(t, "false", sink.Single("enabled"))
	require.JSONEq(t, `[{"name":"cli","project_type":"go","artifact_name":"cli-1.2.3-snapshot-manual-branch-aaaaaaa"}]`, sink.Single("files"))

	in := input(t, filesDeclaration)
	plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.Empty(t, plan.Files)
}

// A file output answers for its Build SBOM as a producer does, and a name the
// plan builds nothing under is refused rather than answered.
func TestBuildSBOMOfAFileOutput(t *testing.T) {
	enabled, err := appsnapshot.BuildSBOM(filesPlan(t, filesDeclaration), "cli")
	require.NoError(t, err)
	require.False(t, enabled)

	plan := filesPlan(t, strings.Replace(filesDeclaration, "    sboms: none\n", "", 1))
	enabled, err = appsnapshot.BuildSBOM(plan, "cli")
	require.NoError(t, err)
	require.True(t, enabled)

	_, err = appsnapshot.BuildSBOM(plan, "absent")
	require.Error(t, err)
}

// The files of an image are the image's: an artifact compiled in a
// Containerfile has no file to keep, while one an image takes is kept beside
// the image when the caller asks.
func TestFilesBesideImages(t *testing.T) {
	in := input(t, "artifacts:\n  - name: inside\n    project-type: go\n    config:\n      build-mode: container-first\ncontainers:\n  - name: app\n    from: [inside]\n")
	in.PublishFiles = true
	plan, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.Empty(t, plan.Files)

	in = input(t, "artifacts:\n  - name: server\n    project-type: go\ncontainers:\n  - name: app\n    from: [server]\n")
	in.PublishFiles = true
	plan, err = appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.True(t, plan.Enabled)
	require.Len(t, plan.Artifacts, 1)
	require.Len(t, plan.Files, 1)

	// Only a plan without an image does without the image repository.
	in.ImageRepository = ""
	_, err = appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.ErrorContains(t, err, "snapshot requires a full base OCI repository")
}

// An artifact that builds a file no snapshot producer can build is refused,
// not left out without a word.
func TestFilesRefuseAnArtifactWithoutSnapshotBuild(t *testing.T) {
	t.Chdir(t.TempDir())

	in := input(t, "artifacts:\n  - name: app\n    project-type: xcode-ios\n    config:\n      scheme: App\n      xcode-version: '16.0'\n      enable-code-signing: false\n")
	in.PublishFiles = true

	_, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.ErrorContains(t, err, "artifact app (xcode-ios) has no snapshot build")
}

// A frozen plan's file outputs are admitted again at every verb.
func TestFrozenFileOutputsRejectMutation(t *testing.T) {
	for name, mutate := range map[string]func(*snapshotimage.Plan){
		"producer":  func(p *snapshotimage.Plan) { p.Files[0].BuildArtifactName = "other" },
		"type":      func(p *snapshotimage.Plan) { p.Files[0].ProjectType = "meta" },
		"duplicate": func(p *snapshotimage.Plan) { p.Files = append(p.Files, p.Files[0]) },
		"tag":       func(p *snapshotimage.Plan) { p.Tag = "1.2.3" },
		"commit":    func(p *snapshotimage.Plan) { p.SourceSHA = strings.Repeat("b", 40) },
		"timestamp": func(p *snapshotimage.Plan) { p.SourceDateEpoch = "" },
	} {
		plan := filesPlan(t, filesDeclaration)
		require.NoError(t, plan.Validate(), name)
		mutate(&plan)
		require.Error(t, plan.Validate(), name)
	}
}

// The bundle is the built files of every declared platform and a receipt
// that binds them to the source commit and says they are unsigned. The build
// gets no credential of the job.
func TestBuildFilesWritesAnUnsignedBundle(t *testing.T) {
	plan := filesPlan(t, filesDeclaration)
	ops := &git{head: plan.SourceSHA}

	runner := &artifactRunner{run: func(appsnapshot.ArtifactBuildRequest) error {
		for _, platform := range []string{"linux-amd64", "darwin-arm64"} {
			require.NoError(t, os.MkdirAll(filepath.Join("dist", platform), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join("dist", platform, "cli-"+platform), []byte("built for "+platform), 0o700))
		}

		return nil
	}}

	err := appsnapshot.BuildFiles(t.Context(), ops, runner, plan, appsnapshot.BuildFilesInput{Name: "other"})
	require.ErrorContains(t, err, "unplanned snapshot file output")
	require.Empty(t, runner.requests)

	in := appsnapshot.BuildFilesInput{Name: "cli", Environment: []string{"PATH=/tools", "GITHUB_TOKEN=secret", "REGISTRY_PASSWORD=secret", "COSIGN_KEY=secret", "RELEASE_TOKEN=secret"}}
	require.NoError(t, appsnapshot.BuildFiles(t.Context(), ops, runner, plan, in))
	require.Len(t, runner.requests, 1)
	require.NotContains(t, strings.Join(runner.requests[0].Env, "\n"), "secret")
	require.Contains(t, runner.requests[0].Args, "linux/amd64,darwin/arm64")
	require.Contains(t, runner.requests[0].Args, "1.2.3-snapshot-manual-branch-aaaaaaa")

	var receipt snapshotimage.FilesReceipt
	require.NoError(t, appsnapshot.ReadJSON("snapshot-files/cli/snapshot-files.json", &receipt))
	require.Equal(t, snapshotimage.FilesReceipt{
		Version: 1, SourceSHA: plan.SourceSHA, SourceBranch: "manual-branch", SnapshotVersion: "1.2.3-snapshot-manual-branch-aaaaaaa",
		Name: "cli", ProjectType: "go", Signing: "NONE",
		Files: []snapshotimage.DependencyFile{
			{Path: "darwin-arm64/cli-darwin-arm64", SHA256: digest("built for darwin-arm64"), Size: 22, Mode: "0755"},
			{Path: "linux-amd64/cli-linux-amd64", SHA256: digest("built for linux-amd64"), Size: 21, Mode: "0755"},
		},
	}, receipt)

	body, err := os.ReadFile("snapshot-files/cli/linux-amd64/cli-linux-amd64")
	require.NoError(t, err)
	require.Equal(t, "built for linux-amd64", string(body))

	entries, err := os.ReadDir("snapshot-files/cli")
	require.NoError(t, err)
	require.Len(t, entries, 3)

	// A second bundle never replaces the first.
	require.ErrorContains(t, appsnapshot.BuildFiles(t.Context(), ops, runner, plan, in), "already exists")
}

func digest(body string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
}

// A reactor's jars are found where the release route finds them, in every
// module's build directory, and kept under the module's path.
func TestBuildFilesKeepsTheJarsOfEveryModule(t *testing.T) {
	plan := filesPlan(t, "artifacts:\n  - name: app\n    project-type: maven\n    sboms: none\n")

	runner := &artifactRunner{run: func(appsnapshot.ArtifactBuildRequest) error {
		for path, body := range map[string]string{
			"api/target/api.jar": "api", "service/target/service.jar": "service",
			"service/target/original-service.jar": "unshaded", "service/target/classes/x.jar": "class output", "docs/site.jar": "not built",
			"web/target/web.war": "web", "parent/target/parent.pom": "pom packaging", "buildSrc/target/buildSrc.jar": "build logic",
		} {
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		}

		return nil
	}}

	require.NoError(t, appsnapshot.BuildFiles(t.Context(), &git{head: plan.SourceSHA}, runner, plan, appsnapshot.BuildFilesInput{Name: "app"}))

	var receipt snapshotimage.FilesReceipt
	require.NoError(t, appsnapshot.ReadJSON("snapshot-files/app/snapshot-files.json", &receipt))
	require.Equal(t, []snapshotimage.DependencyFile{
		{Path: "api/api.jar", SHA256: digest("api"), Size: 3, Mode: "0644"},
		{Path: "service/service.jar", SHA256: digest("service"), Size: 7, Mode: "0644"},
		{Path: "web/web.war", SHA256: digest("web"), Size: 3, Mode: "0644"},
	}, receipt.Files)
	require.FileExists(t, "snapshot-files/app/service/service.jar")
}

// A build that leaves nothing behind publishes no empty bundle.
func TestBuildFilesRefusesABuildWithoutFiles(t *testing.T) {
	plan := filesPlan(t, "artifacts:\n  - name: lib\n    project-type: maven\n    sboms: none\n")

	runner := &artifactRunner{run: func(appsnapshot.ArtifactBuildRequest) error {
		require.NoError(t, os.MkdirAll("target/classes", 0o700))

		return os.WriteFile("target/classes/Lib.class", []byte("class"), 0o600)
	}}

	err := appsnapshot.BuildFiles(t.Context(), &git{head: plan.SourceSHA}, runner, plan, appsnapshot.BuildFilesInput{Name: "lib"})
	require.ErrorContains(t, err, "a project of pom packaging alone has no file to snapshot")
	require.NoDirExists(t, "snapshot-files")
}
