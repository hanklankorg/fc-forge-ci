// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package sbom_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	appsbom "github.com/diggsweden/forge-ci/internal/app/sbom"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// lockfileGenerator writes the bom.json a lockfile-reading tool writes, and
// records the artifacts it ran for.
type lockfileGenerator struct{ ran []string }

func (g *lockfileGenerator) GenerateBuildSBOM(_ context.Context, _ projecttype.Type, dir, name string, _ io.Writer) error {
	g.ran = append(g.ran, name)

	return os.WriteFile(filepath.Join(dir, "bom.json"), []byte(`{"bomFormat":"CycloneDX"}`), 0o600)
}

// Each container-first artifact of the job's toolchain gets its build SBOM
// from its own directory, under the name the build layer has on GitHub,
// the version without its v; one of another toolchain is left to its job.
func TestContainerFirstBuildSBOMs(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	for _, dir := range []string{"svc", "worker", "gosvc"} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	gen := &lockfileGenerator{}

	written, err := appsbom.ContainerFirstBuildSBOMs(context.Background(), &fakeGit{sha: "abc1234"}, gen, io.Discard, io.Discard, appsbom.ContainerFirstBuildSBOMsInput{
		Artifacts: []pipeline.PlannedArtifact{
			{Name: "svc", ProjectType: projecttype.Cargo, WorkingDirectory: "svc"},
			{Name: "gosvc", ProjectType: projecttype.Go, WorkingDirectory: "gosvc"},
			{Name: "worker", ProjectType: projecttype.Cargo, WorkingDirectory: "worker"},
		},
		ProjectType: projecttype.Cargo,
		Version:     "v1.2.3",
		OutputDir:   "dist/sboms",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"dist/sboms/svc-1.2.3-abc1234-build-sbom.cyclonedx.json", "dist/sboms/worker-1.2.3-abc1234-build-sbom.cyclonedx.json"}
	if !slices.Equal(written, want) || !slices.Equal(gen.ran, []string{"svc", "worker"}) {
		t.Fatalf("wrote %q for %q, want %q for svc and worker", written, gen.ran, want)
	}

	for _, file := range want {
		if _, err := os.Stat(file); err != nil {
			t.Error(err)
		}
	}

	// The generator's bom.json goes; the artifact's directory stays.
	for _, dir := range []string{"svc", "worker"} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Errorf("%s holds %v (%v), want the directory as it was", dir, entries, err)
		}
	}
}

// A bom.json already in an artifact's directory, committed or left by a
// build step, would stand in for the one forge-ci writes from the lockfile.
func TestContainerFirstBuildSBOMsRefusesABOMAlreadyThere(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := os.MkdirAll("svc", 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile("svc/bom.json", []byte(`{"bomFormat":"CycloneDX"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	gen := &lockfileGenerator{}

	_, err := appsbom.ContainerFirstBuildSBOMs(context.Background(), &fakeGit{sha: "abc1234"}, gen, io.Discard, io.Discard, appsbom.ContainerFirstBuildSBOMsInput{
		Artifacts:   []pipeline.PlannedArtifact{{Name: "svc", ProjectType: projecttype.Cargo, WorkingDirectory: "svc"}},
		ProjectType: projecttype.Cargo, Version: "1.2.3", OutputDir: "dist/sboms",
	})
	if !errors.Is(err, errs.ErrValidation) || len(gen.ran) != 0 {
		t.Fatalf("err %v after running for %q, want ErrValidation before any run", err, gen.ran)
	}
}

// Only go and cargo have a lockfile-reading generator, and the release names
// need the version and a place to go.
func TestContainerFirstBuildSBOMsRefusesAnIncompleteCall(t *testing.T) {
	t.Chdir(t.TempDir())

	for name, in := range map[string]appsbom.ContainerFirstBuildSBOMsInput{
		"maven":      {ProjectType: projecttype.Maven, Version: "1.2.3", OutputDir: "dist/sboms"},
		"no version": {ProjectType: projecttype.Go, OutputDir: "dist/sboms"},
		"no output":  {ProjectType: projecttype.Go, Version: "1.2.3"},
	} {
		if _, err := appsbom.ContainerFirstBuildSBOMs(context.Background(), &fakeGit{}, &lockfileGenerator{}, io.Discard, io.Discard, in); err == nil {
			t.Errorf("%s: no refusal", name)
		}
	}
}
