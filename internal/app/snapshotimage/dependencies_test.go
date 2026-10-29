// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/ociregistry"
	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/stretchr/testify/require"
)

type countingBuilder struct{ calls int }

var errBuilderReached = errors.New("builder reached")

func (b *countingBuilder) BuildSignerImage(context.Context, container.SignerImageBuildToolRequest, io.Writer) error {
	b.calls++
	return errBuilderReached
}
func (*countingBuilder) ExportLocalImageToLayout(context.Context, string, string, io.Writer) error {
	panic("unexpected export")
}
func (*countingBuilder) PushLayout(context.Context, string, string, string) (string, error) {
	panic("unexpected upload")
}
func (*countingBuilder) RawManifest(context.Context, string, string, io.Writer) ([]byte, error) {
	panic("unexpected registry read")
}

func dependencyFixture(t *testing.T) (snapshotimage.Plan, appsnapshot.BuildInput) {
	t.Helper()
	t.Chdir(t.TempDir())
	in := input(t, "artifacts:\n  - name: server\n    project-type: go\ncontainers:\n  - name: app\n    from: [server]\n    platforms: linux/amd64\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("Containerfile", []byte("FROM scratch\nCOPY dist /dist\n"), 0o600))
	require.NoError(t, os.MkdirAll("dist/linux-amd64", 0o700))
	require.NoError(t, os.WriteFile("dist/linux-amd64/server-linux-amd64", []byte("producer-built bytes"), 0o700))
	require.NoError(t, appsnapshot.RecordContext(t.Context(), &git{head: p.SourceSHA}, p, snapshotimage.DefaultDependenciesFile))
	require.NoError(t, os.MkdirAll("metadata/app-arch-amd64", 0o700))
	require.NoError(t, os.WriteFile("metadata/app-arch-amd64/keep", []byte("prior metadata"), 0o600))
	return p, appsnapshot.BuildInput{Name: "app", Arch: "amd64", HostArch: "amd64", MetadataDir: "metadata", ServerURL: "https://forge.example", Repository: "team/app"}
}

func TestDependencyInventoryValidatesBeforeBuilderOrMetadataMutation(t *testing.T) {
	for _, defect := range []string{"foreign source", "wrong artifact", "wrong platform", "substituted bytes", "extra file", "missing file", "symlink", "fifo", "unknown field", "trailing document", "duplicate field"} {
		t.Run(defect, func(t *testing.T) {
			p, in := dependencyFixture(t)
			var inventory snapshotimage.DependencyInventory
			require.NoError(t, appsnapshot.ReadJSON(snapshotimage.DefaultDependenciesFile, &inventory))
			switch defect {
			case "foreign source":
				inventory.SourceSHA = strings.Repeat("b", 40)
			case "wrong artifact":
				inventory.Dependencies[0].ArtifactName = "other-go-build-artifacts"
			case "wrong platform":
				inventory.Dependencies[0].Platform = "linux/arm64"
			case "substituted bytes":
				require.NoError(t, os.WriteFile("dist/linux-amd64/server-linux-amd64", []byte(strings.Repeat("x", len("producer-built bytes"))), 0o700))
			case "extra file":
				require.NoError(t, os.WriteFile("dist/unrecorded", []byte("extra"), 0o600))
			case "missing file":
				require.NoError(t, os.Remove("dist/linux-amd64/server-linux-amd64"))
			case "symlink":
				require.NoError(t, os.Rename(snapshotimage.DefaultDependenciesFile, "real.json"))
				require.NoError(t, os.Symlink("real.json", snapshotimage.DefaultDependenciesFile))
			case "fifo":
				require.NoError(t, os.Remove(snapshotimage.DefaultDependenciesFile))
				require.NoError(t, syscall.Mkfifo(snapshotimage.DefaultDependenciesFile, 0o600))
			}
			if defect == "foreign source" || defect == "wrong artifact" || defect == "wrong platform" {
				require.NoError(t, appsnapshot.WriteJSON(snapshotimage.DefaultDependenciesFile, inventory))
			}
			if defect == "unknown field" || defect == "trailing document" || defect == "duplicate field" {
				body, err := os.ReadFile(snapshotimage.DefaultDependenciesFile)
				require.NoError(t, err)
				switch defect {
				case "unknown field":
					body = append([]byte(`{"unexpected":true,`), body[1:]...)
				case "trailing document":
					body = append(body, []byte(`{}`)...)
				case "duplicate field":
					body = append([]byte(`{"version":1,`), body[1:]...)
				}
				require.NoError(t, os.WriteFile(snapshotimage.DefaultDependenciesFile, body, 0o600))
			}
			builder := &countingBuilder{}
			err := appsnapshot.Build(t.Context(), &git{head: p.SourceSHA}, builder, ociregistry.NewAnonymous(), io.Discard, output.Annotator{}, p, in)
			require.Error(t, err)
			require.Zero(t, builder.calls)
			body, err := os.ReadFile("metadata/app-arch-amd64/keep")
			require.NoError(t, err)
			require.Equal(t, "prior metadata", string(body))
		})
	}
}

func TestRecordedContextAllowsExactInputsAndExplicitInventoryPath(t *testing.T) {
	p, in := dependencyFixture(t)
	in.DependenciesFile = "context-inventory.json"
	require.NoError(t, os.Rename(snapshotimage.DefaultDependenciesFile, in.DependenciesFile))
	builder := &countingBuilder{}
	err := appsnapshot.Build(t.Context(), &git{head: p.SourceSHA}, builder, ociregistry.NewAnonymous(), io.Discard, output.Annotator{}, p, in)
	require.ErrorIs(t, err, errBuilderReached)
	require.Equal(t, 1, builder.calls)
}

func TestBuildRequiresSourceInventoryEvenWithoutArtifactDependencies(t *testing.T) {
	t.Chdir(t.TempDir())
	in := input(t, "containers:\n  - name: app\n    platforms: linux/amd64\n")
	p, err := appsnapshot.Plan(t.Context(), &git{head: in.SourceSHA}, in)
	require.NoError(t, err)
	build := appsnapshot.BuildInput{Name: "app", Arch: "amd64", HostArch: "amd64", MetadataDir: "metadata", ServerURL: "https://forge.example", Repository: "team/app"}
	builder := &countingBuilder{}
	err = appsnapshot.Build(t.Context(), &git{head: p.SourceSHA}, builder, ociregistry.NewAnonymous(), io.Discard, output.Annotator{}, p, build)
	require.ErrorContains(t, err, "read snapshot dependencies")
	require.Zero(t, builder.calls)
	require.NoError(t, appsnapshot.RecordContext(t.Context(), &git{head: p.SourceSHA}, p, snapshotimage.DefaultDependenciesFile))
	var inventory snapshotimage.DependencyInventory
	require.NoError(t, appsnapshot.ReadJSON(snapshotimage.DefaultDependenciesFile, &inventory))
	require.Empty(t, inventory.Dependencies)
	require.Equal(t, p.SourceSHA, inventory.SourceSHA)
	err = appsnapshot.Build(t.Context(), &git{head: p.SourceSHA}, builder, ociregistry.NewAnonymous(), io.Discard, output.Annotator{}, p, build)
	require.ErrorIs(t, err, errBuilderReached)
	require.Equal(t, 1, builder.calls)
}

func TestSnapshotJSONRejectsSymlinkPathsAndPreservesOutputs(t *testing.T) {
	p, _ := dependencyFixture(t)
	require.NoError(t, appsnapshot.WriteJSON("plan.json", p))
	prior, err := os.ReadFile("plan.json")
	require.NoError(t, err)
	require.NoError(t, os.Symlink("plan.json", "plan-link.json"))
	_, err = appsnapshot.ReadPlan("plan-link.json")
	require.Error(t, err)
	require.Error(t, appsnapshot.WriteJSON("plan-link.json", p))
	after, err := os.ReadFile("plan.json")
	require.NoError(t, err)
	require.Equal(t, prior, after)
	require.NoError(t, os.Mkdir("real-dir", 0o700))
	require.NoError(t, appsnapshot.WriteJSON("real-dir/plan.json", p))
	require.NoError(t, os.Symlink("real-dir", "linked-dir"))
	_, err = appsnapshot.ReadPlan("linked-dir/plan.json")
	require.Error(t, err)
	require.Error(t, appsnapshot.WriteJSON("linked-dir/plan.json", p))
	require.NoError(t, syscall.Mkfifo("output-fifo", 0o600))
	require.Error(t, appsnapshot.WriteJSON("output-fifo", p))
}

func TestContextRefusesUntrustedFilesAndPreservesPriorManifest(t *testing.T) {
	for _, defect := range []string{"foreign HEAD", "symlink file", "symlink ancestor", "fifo file", "self inclusion"} {
		t.Run(defect, func(t *testing.T) {
			p, _ := dependencyFixture(t)
			ops := &git{head: p.SourceSHA}
			destination := snapshotimage.DefaultDependenciesFile
			prior, err := os.ReadFile(destination)
			require.NoError(t, err)
			switch defect {
			case "foreign HEAD":
				ops.head = strings.Repeat("b", 40)
			case "symlink file":
				require.NoError(t, os.Symlink("server-linux-amd64", "dist/linux-amd64/link"))
			case "fifo file":
				require.NoError(t, syscall.Mkfifo("dist/linux-amd64/fifo", 0o600))
			case "symlink ancestor":
				require.NoError(t, os.Rename("dist", "real-dist"))
				require.NoError(t, os.Symlink("real-dist", "dist"))
			case "self inclusion":
				destination = filepath.Join("dist", "snapshot-dependencies.json")
			}
			err = appsnapshot.RecordContext(t.Context(), ops, p, destination)
			require.Error(t, err)
			after, err := os.ReadFile(snapshotimage.DefaultDependenciesFile)
			require.NoError(t, err)
			require.Equal(t, prior, after)
		})
	}
}
