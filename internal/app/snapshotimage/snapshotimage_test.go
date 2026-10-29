// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/gitlaboutput"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/stretchr/testify/require"
)

type git struct {
	head  string
	calls int
}

func (g *git) Run(_ context.Context, args ...string) (string, error) {
	g.calls++
	if len(args) > 0 && args[0] == "show" {
		return "1", nil
	}
	return g.head, nil
}
func (g *git) ListTags(context.Context, string) ([]string, error)    { return []string{"v1.2.3"}, nil }
func (g *git) ShortSHA(context.Context, string, int) (string, error) { return g.head[:7], nil }

func input(t *testing.T, body string) appsnapshot.PlanInput {
	t.Helper()
	file := filepath.Join(t.TempDir(), "artifacts.yml")
	require.NoError(t, os.WriteFile(file, []byte(body), 0o600))
	return appsnapshot.PlanInput{File: file, Branch: "manual-branch", SourceSHA: strings.Repeat("a", 40), ImageRepository: "registry.example/team/app", Publish: true}
}

func TestSourcePinLogicalBranchDefaultOnAndOptOut(t *testing.T) {
	in := input(t, "containers:\n  - name: app\n")
	git := &git{head: in.SourceSHA}
	p, err := appsnapshot.Plan(t.Context(), git, in)
	require.NoError(t, err)
	require.True(t, p.Enabled)
	require.Equal(t, "manual-branch", p.SourceBranch)
	require.Regexp(t, `^1\.2\.3-manual-branch-[0-9a-f]{16}-aaaaaaa-snapshot$`, p.Tag)
	require.True(t, p.Images[0].Scan)
	require.False(t, p.Images[0].Provenance)
	sink := fakeoutputsink.New(t)
	require.NoError(t, appsnapshot.EmitPlan(t.Context(), sink, p))
	require.Contains(t, sink.Single("plan-json"), `"source_branch":"manual-branch"`)
	require.Contains(t, sink.Single("builds"), `"arch":"amd64"`)
	in.Publish = false
	p, err = appsnapshot.Plan(t.Context(), git, in)
	require.NoError(t, err)
	require.False(t, p.Enabled)
	require.Empty(t, p.Builds)
	in.Publish = true
	git.head = strings.Repeat("b", 40)
	git.calls = 0
	_, err = appsnapshot.Plan(t.Context(), git, in)
	require.ErrorContains(t, err, "differs from source-sha")
	require.Equal(t, 1, git.calls)
}

func TestNoDeclaredContainersNeverInfersRootImage(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("Containerfile", []byte("FROM scratch"), 0o600))
	in := input(t, "artifacts:\n  - name: meta\n    project-type: meta\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.False(t, p.Enabled)
	require.Empty(t, p.Images)
	in.File = appsnapshot.DefaultConfigFile
	p, err = appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.False(t, p.Enabled)
	in.File = "absent.yml"
	_, err = appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorContains(t, err, "absent.yml")
}

func TestUnsupportedSnapshotRequestsFailBeforeBuild(t *testing.T) {
	for _, body := range []string{
		"containers:\n  - name: app\n    enable-slsa: true\n",
		"containers:\n  - name: app\n    platforms: linux/s390x\n",
		"sign:\n  method: sigstore\ncontainers:\n  - name: app\n",
	} {
		in := input(t, body)
		_, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
		require.Error(t, err)
	}
}

func TestNativeBuildAndArtifactInputsRefuseBeforeTools(t *testing.T) {
	in := input(t, "artifacts:\n  - name: server\n    project-type: go\ncontainers:\n  - name: app\n    from: [server]\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.Equal(t, "server-go-build-artifacts", p.Builds[0].RequiredArtifacts[0].ArtifactName)
	require.Equal(t, "dist", p.Builds[0].RequiredArtifacts[0].Destination)
	err = appsnapshot.Build(t.Context(), &git{head: in.SourceSHA}, nil, nil, io.Discard, output.Annotator{}, p, appsnapshot.BuildInput{Name: "app", Arch: "amd64", HostArch: "arm64", MetadataDir: t.TempDir()})
	require.ErrorContains(t, err, "native runner")
	// A nil tool would panic if dependency absence were ignored.
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll("dist/linux-arm64", 0o700))
	require.NoError(t, os.WriteFile("dist/linux-arm64/server-linux-arm64", []byte("other native platform"), 0o700))
	err = appsnapshot.Build(t.Context(), &git{head: in.SourceSHA}, nil, nil, io.Discard, output.Annotator{}, p, appsnapshot.BuildInput{Name: "app", Arch: "amd64", HostArch: "amd64", MetadataDir: "metadata"})
	require.ErrorContains(t, err, "read snapshot dependencies")
}

func TestReadPlanStrictAndAtomicReceiptCollection(t *testing.T) {
	in := input(t, "containers:\n  - name: app\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	require.NoError(t, appsnapshot.WriteJSON(path, p))
	got, err := appsnapshot.ReadPlan(path)
	require.NoError(t, err)
	require.Equal(t, p, got)
	require.NoError(t, os.WriteFile(path, []byte(`{"unexpected":true}`), 0o600))
	_, err = appsnapshot.ReadPlan(path)
	require.Error(t, err)
	receipts := t.TempDir()
	output := filepath.Join(dir, "images.json")
	require.NoError(t, os.WriteFile(output, []byte("prior-complete"), 0o600))
	err = appsnapshot.Collect(t.Context(), fakeoutputsink.New(t), p, receipts, output)
	require.Error(t, err)
	body, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "prior-complete", string(body))
	r := snapshotimage.Receipt{Version: 1, SourceSHA: p.SourceSHA, SourceBranch: p.SourceBranch, Name: "app", ImageRepository: p.Images[0].ImageRepository, Tag: p.Images[0].Tag, Digest: "sha256:" + strings.Repeat("b", 64), Platforms: p.Images[0].Platforms}
	r.Ref = r.ImageRepository + "@" + r.Digest
	require.NoError(t, appsnapshot.WriteJSON(filepath.Join(receipts, "app.json"), r))
	sink := fakeoutputsink.New(t)
	require.NoError(t, appsnapshot.Collect(t.Context(), sink, p, receipts, output))
	require.Contains(t, sink.Single("container-images-json"), r.Ref)
}

func TestCollectEmitsPortableGitLabScalarOutputs(t *testing.T) {
	in := input(t, "containers:\n  - name: app\n    platforms: linux/amd64\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	r := snapshotimage.Receipt{Version: 1, SourceSHA: p.SourceSHA, SourceBranch: p.SourceBranch, Name: "app", ImageRepository: p.Images[0].ImageRepository, Tag: p.Images[0].Tag, Digest: "sha256:" + strings.Repeat("b", 64), Platforms: p.Images[0].Platforms}
	r.Ref = r.ImageRepository + "@" + r.Digest
	receipts := t.TempDir()
	require.NoError(t, appsnapshot.WriteJSON(filepath.Join(receipts, "app.json"), r))
	dir := t.TempDir()
	dotenv := filepath.Join(dir, "outputs.env")
	sink := gitlaboutput.New(dotenv)
	t.Cleanup(func() { _ = sink.Close(context.Background()) })
	require.NoError(t, appsnapshot.Collect(t.Context(), sink, p, receipts, filepath.Join(dir, "images.json")))
	require.NoError(t, sink.Close(t.Context()))
	body, err := os.ReadFile(dotenv)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	require.Len(t, lines, 2, "each provider output must occupy exactly one dotenv line")
	value, found := strings.CutPrefix(lines[1], "DIGESTREFS=")
	require.True(t, found)
	var refs []string
	require.NoError(t, json.Unmarshal([]byte(value), &refs))
	require.Equal(t, []string{r.Ref}, refs)
}
