// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

// The shipped fixture exercises a script library's meta declaration.
func TestMetaRelease_NoBuildOrVersionBumpAndNoImplicitSourceAttachments(t *testing.T) {
	configPlan := metaConfigPlan(t)

	plan, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
		ConfigPlan: configPlan, RefName: "v0.2.0", ChangelogCreator: "git-cliff",
		ChangelogSkipVersionBump: true, ReleasePublisher: "github-cli", ReleaseSignArtifacts: true, ReleaseSBOMs: "all",
	})
	if err != nil {
		t.Fatal(err)
	}

	checkMetaReleaseAssembly(t, configPlan, plan)
}

func metaConfigPlan(t *testing.T) pipeline.ConfigPlan {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-meta/consumer/.forge-ci/artifacts.yml"))
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	configPlan := pipeline.NewConfigPlan(cfg)
	if len(configPlan.Artifacts.Meta) != 1 || configPlan.FallbackProjectType != projecttype.Meta || configPlan.PipelineSBOMs != "none" {
		t.Fatalf("not a changelog-only meta plan: %+v", configPlan)
	}

	return configPlan
}

func checkMetaReleaseAssembly(t *testing.T, configPlan pipeline.ConfigPlan, plan pipeline.ReleasePlan) {
	t.Helper()

	if plan.Policy.RunVersionBump || plan.Stages.Prepare.Targets.VersionBump.Runs || !plan.Policy.CreateRelease || !plan.Policy.SignArtifacts || plan.Policy.SBOMs != "none" || plan.Policy.HasContainers {
		t.Fatalf("meta release policy changed: %+v", plan)
	}

	build := plan.Stages.Build.Targets
	if build.Maven.Runs || build.NPM.Runs || build.Gradle.Runs || build.GradleAndroid.Runs || build.XcodeIOS.Runs || build.Go.Runs || build.Cargo.Runs {
		t.Fatalf("meta must not invent a build: %+v", build)
	}

	if len(plan.ArtifactTransfers.Items) != 0 {
		t.Fatalf("meta must not fetch build products: %+v", plan.ArtifactTransfers)
	}

	fsys := testfs.NewReal(t)
	t.Chdir(fsys.Root)

	for _, name := range []string{"install.sh", "lib/check.sh", ".config/tool/defaults", ".github/artifacts.yml", "release-artifacts/fake.tar.gz"} {
		fsys.WriteFile(name, []byte("source, not an authorized release attachment\n"))
	}

	acquired := t.TempDir()

	asm, err := apprelease.Assemble(&bytes.Buffer{}, apprelease.AssembleInput{
		ConfigPlanJSON: mustReleaseJSON(t, configPlan), ArtifactTransferPlanJSON: mustReleaseJSON(t, plan.ArtifactTransfers),
		ProjectName: "hello-meta", Version: "v0.2.0", AcquisitionDir: acquired,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(asm.Assets) != 0 || len(asm.SBOMs) != 0 {
		t.Fatalf("source checkout crossed the explicit attachment boundary: %+v", asm)
	}

	count, err := apprelease.Checksums(&bytes.Buffer{}, apprelease.ChecksumsInput{AssemblyFile: ".forge-ci/release-assembly.json"})
	if err != nil || count != 0 {
		t.Fatalf("meta checksums = %d, %v; want the native empty checksum asset", count, err)
	}

	for _, name := range []string{"install.sh", "lib/check.sh", ".config/tool/defaults", ".github/artifacts.yml"} {
		if got, err := os.ReadFile(name); err != nil || string(got) != "source, not an authorized release attachment\n" {
			t.Fatalf("source %s changed: %q, %v", name, got, err)
		}
	}
}

// This characterizes the missing product unit, not successful SNAPSHOT output.
// A meta declaration is admitted today but cannot describe a file producer.
func TestMetaSnapshotDeclarationHasNoFileTransfer(t *testing.T) {
	plan, err := pipeline.NewSnapshotReleasePlan(pipeline.SnapshotReleasePlanInput{ConfigPlan: metaConfigPlan(t), Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Context.ProjectType != projecttype.Meta || len(plan.ArtifactTransfers.Items) != 0 {
		t.Fatalf("revisit the documented meta file-snapshot gap: %+v", plan)
	}

	build := plan.Stages.Build.Targets
	if build.Maven.Runs || build.NPM.Runs || build.Gradle.Runs || build.GradleAndroid.Runs || build.XcodeIOS.Runs || build.Go.Runs || build.Cargo.Runs {
		t.Fatalf("meta snapshot invented a compiled build: %+v", build)
	}
}
