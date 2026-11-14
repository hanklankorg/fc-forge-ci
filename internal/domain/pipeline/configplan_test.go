// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

func TestNewConfigPlan_GroupsArtifactsAndDefaultsContainers(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Artifacts: []config.Artifact{
			{Name: "app", ProjectType: projecttype.Maven, WorkingDirectory: "services/app", BuildType: config.BuildTypeApplication, PublishTo: []config.PublishTarget{config.PublishForgePackages}},
			{Name: "lib", ProjectType: projecttype.Maven, BuildType: config.BuildTypeLibrary, PublishTo: []config.PublishTarget{config.PublishForgePackages, config.PublishMavenCentral}},
			{Name: "pkg", ProjectType: projecttype.NPM, PublishTo: []config.PublishTarget{config.PublishForgePackages, config.PublishNPMJS}, NPM: &config.NPMConfig{Tag: "release-{version}"}},
			{Name: "jvm-app", ProjectType: projecttype.Gradle, WorkingDirectory: "services/jvm-app"},
			{Name: "go-cli", ProjectType: projecttype.Go, Go: &config.GoConfig{BuildMode: config.GoBuildModeArtifactFirst}},
			{Name: "go-service", ProjectType: projecttype.Go, Go: &config.GoConfig{BuildMode: config.GoBuildModeContainerFirst}},
		},
		Containers: []config.Container{{
			Name: "image",
			From: []string{"app", "pkg", "jvm-app", "go-cli"},
		}},
	}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan := pipeline.NewConfigPlan(cfg)

	if plan.Version != pipeline.ConfigPlanVersion {
		t.Errorf("version = %d", plan.Version)
	}

	if plan.FallbackProjectType != projecttype.Maven {
		t.Errorf("fallback project type = %q", plan.FallbackProjectType)
	}

	if got := names(plan.Artifacts.ForgePackages); !slices.Equal(got, []string{"lib", "pkg"}) {
		t.Errorf("github packages = %v", got)
	}

	if got := names(plan.Artifacts.MavenCentral); !slices.Equal(got, []string{"lib"}) {
		t.Errorf("maven central = %v", got)
	}

	if got := names(plan.Artifacts.NPMJS); len(got) != 0 {
		t.Errorf("npmjs should be unsupported for current workflows, got %v", got)
	}

	if got := names(plan.Artifacts.GoArtifactFirst); !slices.Equal(got, []string{"go-cli"}) {
		t.Errorf("go artifact first = %v", got)
	}

	if got := plan.Artifacts.Maven[0].BuildArtifactName; got != "app-build-artifacts" {
		t.Errorf("maven build artifact name = %q", got)
	}

	if got := plan.Artifacts.Maven[0].BuildSBOMArtifactName; got != "app-build-sbom" {
		t.Errorf("maven build sbom artifact name = %q", got)
	}

	if got := plan.Artifacts.NPM[0].BuildArtifactName; got != "pkg-build-artifacts" {
		t.Errorf("npm build artifact name = %q", got)
	}

	if got := plan.Artifacts.GoArtifactFirst[0].BuildArtifactName; got != "go-cli-go-build-artifacts" {
		t.Errorf("go build artifact name = %q", got)
	}

	if got := names(plan.Artifacts.GoContainerFirst); !slices.Equal(got, []string{"go-service"}) {
		t.Errorf("go container first = %v", got)
	}

	if !plan.Containers.HasContainers || len(plan.Containers.All) != 1 {
		t.Fatalf("containers = %+v", plan.Containers)
	}

	plannedContainer := plan.Containers.All[0]
	if plannedContainer.ContainerFile != "Containerfile" || plannedContainer.Context != "." || plannedContainer.Platforms != "linux/amd64" {
		t.Errorf("container defaults = %+v", plannedContainer)
	}

	if plannedContainer.GoArtifactName != "go-cli" {
		t.Errorf("go artifact name = %q", plannedContainer.GoArtifactName)
	}

	if plannedContainer.MavenArtifactName != "app-build-artifacts" {
		t.Errorf("maven artifact name = %q", plannedContainer.MavenArtifactName)
	}

	// The image build restores the artifact where it was built.
	if plannedContainer.MavenWorkingDirectory != "services/app" {
		t.Errorf("maven working directory = %q", plannedContainer.MavenWorkingDirectory)
	}

	// What the planner writes, the consuming job's validation admits.
	if err := pipeline.ValidateConfigPlan(plan); err != nil {
		t.Errorf("the planner's own plan is refused: %v", err)
	}

	if plannedContainer.NPMArtifactName != "pkg-build-artifacts" {
		t.Errorf("npm artifact name = %q", plannedContainer.NPMArtifactName)
	}

	if plannedContainer.GradleArtifactName != "jvm-app-build-artifacts" {
		t.Errorf("gradle artifact name = %q", plannedContainer.GradleArtifactName)
	}

	if plannedContainer.GradleWorkingDirectory != "services/jvm-app" {
		t.Errorf("gradle working directory = %q", plannedContainer.GradleWorkingDirectory)
	}

	// Per-container gates default true when artifacts.yml doesn't set them.
	if !plannedContainer.EnableSLSA {
		t.Errorf("container gates should default true; got EnableSLSA=%v", plannedContainer.EnableSLSA)
	}

	if plannedContainer.ScanSeverity != "" {
		t.Errorf("ScanSeverity should be empty when unset (caller default applies); got %q", plannedContainer.ScanSeverity)
	}
}

// TestNewConfigPlan_ContainerGatesPropagateExplicitFalse pins the
// per-container override path that was dead before this slice:
// artifacts.yml `enable-slsa: false` /
// `scan-severity: CRITICAL` reach release-publish-stage.yml via the
// typed PlannedContainer rather than the previous hardcoded `true`.
func TestNewConfigPlan_ContainerGatesPropagateExplicitFalse(t *testing.T) {
	t.Parallel()

	no := false

	cfg := &config.Config{
		Artifacts: []config.Artifact{
			{Name: "go-cli", ProjectType: projecttype.Go, Go: &config.GoConfig{BuildMode: config.GoBuildModeArtifactFirst}},
		},
		Containers: []config.Container{{
			Name:         "image",
			From:         []string{"go-cli"},
			EnableSLSA:   &no,
			ScanSeverity: "CRITICAL",
		}},
	}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plannedContainer := pipeline.NewConfigPlan(cfg).Containers.All[0]

	if plannedContainer.EnableSLSA {
		t.Errorf("EnableSLSA=true, want false (explicit override)")
	}

	if plannedContainer.ScanSeverity != "CRITICAL" {
		t.Errorf("ScanSeverity=%q, want CRITICAL", plannedContainer.ScanSeverity)
	}
}

// TestNewConfigPlan_BuildSecretsPropagated pins the new
// containers[].build-secrets field flowing through to PlannedContainer.
// This is the foot-gun mitigation for "secrets via build-args ending up
// in public SLSA provenance" — adopters declare names here, the
// workflow plumbs them to BuildKit secret mounts which are never
// recorded in provenance.
func TestNewConfigPlan_BuildSecretsPropagated(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Artifacts: []config.Artifact{{Name: "app", ProjectType: projecttype.Maven}},
		Containers: []config.Container{
			{Name: "with-secrets", From: []string{"app"}, BuildSecrets: []string{"DB_PASSWORD", "API_TOKEN"}},
			{Name: "no-secrets", From: []string{"app"}},
		},
	}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan := pipeline.NewConfigPlan(cfg)

	byName := map[string]pipeline.PlannedContainer{}
	for _, c := range plan.Containers.All {
		byName[c.Name] = c
	}

	if got := byName["with-secrets"].BuildSecrets; !slices.Equal(got, []string{"DB_PASSWORD", "API_TOKEN"}) {
		t.Errorf("with-secrets BuildSecrets = %v", got)
	}

	if got := byName["no-secrets"].BuildSecrets; len(got) != 0 {
		t.Errorf("no-secrets BuildSecrets should be empty, got %v", got)
	}
}

func TestNewConfigPlan_BuildArtifactNamesMatchConditionalUploads(t *testing.T) {
	t.Parallel()

	yes, no := true, false

	cfg := &config.Config{Artifacts: []config.Artifact{
		{Name: "android-aab", ProjectType: projecttype.GradleAndroid, GradleAndroid: &config.GradleAndroidConfig{IncludeAAB: &yes, BuildTypes: "release"}},
		{Name: "android-no-aab", ProjectType: projecttype.GradleAndroid, GradleAndroid: &config.GradleAndroidConfig{IncludeAAB: &no, BuildTypes: "release"}},
		{Name: "android-debug", ProjectType: projecttype.GradleAndroid, GradleAndroid: &config.GradleAndroidConfig{IncludeAAB: &yes, BuildTypes: "debug"}},
		{Name: "ios-signed", ProjectType: projecttype.XcodeIOS, XcodeIOS: &config.XcodeIOSConfig{EnableCodeSigning: &yes}},
		{Name: "ios-unsigned", ProjectType: projecttype.XcodeIOS, XcodeIOS: &config.XcodeIOSConfig{EnableCodeSigning: &no}},
	}}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan := pipeline.NewConfigPlan(cfg)

	byName := map[string]pipeline.PlannedArtifact{}
	for _, artifact := range plan.Artifacts.All {
		byName[artifact.Name] = artifact
	}

	for name, want := range map[string]string{
		"android-aab":    "android-aab",
		"android-no-aab": "",
		"android-debug":  "",
		"ios-signed":     "ios-signed",
		"ios-unsigned":   "ios-unsigned-archive",
	} {
		if got := byName[name].BuildArtifactName; got != want {
			t.Errorf("%s build artifact name = %q, want %q", name, got, want)
		}
	}
}

func TestNewConfigPlan_AndroidReleaseNameDoesNotUseSubstringMatching(t *testing.T) {
	t.Parallel()

	yes := true
	cfg := &config.Config{Artifacts: []config.Artifact{{
		Name: "android-lookalike", ProjectType: projecttype.GradleAndroid,
		GradleAndroid: &config.GradleAndroidConfig{IncludeAAB: &yes, BuildTypes: "notrelease"},
	}}}

	plan := pipeline.NewConfigPlan(cfg)
	if got := plan.Artifacts.All[0].BuildArtifactName; got != "" {
		t.Fatalf("substring build type produced release artifact name %q", got)
	}
}

// TestNewConfigPlan_CargoArtifactNameOnContainer pins the Go/Cargo
// symmetry for the container `from:` referent — when a container
// consumes an artifact-first cargo artifact, the planner exposes its
// upload name via Container.CargoArtifactName so publish-container.yml
// can `actions/download-artifact` it into the build context. Missing
// this slot would mean the documented artifact-first cargo → container
// path silently no-ops (the container build sees no binary).
func TestNewConfigPlan_CargoArtifactNameOnContainer(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Artifacts: []config.Artifact{
			{Name: "cli", ProjectType: projecttype.Cargo, Cargo: &config.CargoConfig{BuildMode: config.CargoBuildModeArtifactFirst}},
			{Name: "svc", ProjectType: projecttype.Cargo, Cargo: &config.CargoConfig{BuildMode: config.CargoBuildModeContainerFirst}},
		},
		Containers: []config.Container{
			{Name: "cli-image", From: []string{"cli"}},
			{Name: "svc-image", From: []string{"svc"}},
		},
	}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan := pipeline.NewConfigPlan(cfg)

	byName := map[string]pipeline.PlannedContainer{}
	for _, container := range plan.Containers.All {
		byName[container.Name] = container
	}

	if got := byName["cli-image"].CargoArtifactName; got != "cli" {
		t.Errorf("artifact-first cargo dep: CargoArtifactName = %q, want %q", got, "cli")
	}

	if got := byName["svc-image"].CargoArtifactName; got != "" {
		t.Errorf("container-first cargo dep: CargoArtifactName = %q, want empty", got)
	}
}

// TestNewConfigPlan_CargoBuildArtifactGatedByBuildMode pins the Go/Cargo
// symmetry for BuildArtifactName: artifact-first emits a real upload
// name (so release-create-github transfers the binary back to the
// GitHub release); container-first emits "" (no build-stage upload — the
// binary stays inside the container build or ships via extract.binary).
//
// Regression guard for the bug where ResolveArtifactNames(Cargo) used
// to return the SBOM name in the BuildArtifact slot, leaving
// artifact-first cargo binaries silently absent from the transfer plan.
func TestNewConfigPlan_CargoBuildArtifactGatedByBuildMode(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Artifacts: []config.Artifact{
		{Name: "cli", ProjectType: projecttype.Cargo, Cargo: &config.CargoConfig{BuildMode: config.CargoBuildModeArtifactFirst}},
		{Name: "svc", ProjectType: projecttype.Cargo, Cargo: &config.CargoConfig{BuildMode: config.CargoBuildModeContainerFirst}},
	}}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan := pipeline.NewConfigPlan(cfg)

	byName := map[string]pipeline.PlannedArtifact{}
	for _, artifact := range plan.Artifacts.All {
		byName[artifact.Name] = artifact
	}

	if got := byName["cli"].BuildArtifactName; got != "cli-cargo-build-artifacts" {
		t.Errorf("artifact-first cargo BuildArtifactName = %q, want %q", got, "cli-cargo-build-artifacts")
	}

	if got := byName["svc"].BuildArtifactName; got != "" {
		t.Errorf("container-first cargo BuildArtifactName = %q, want empty", got)
	}

	// Both modes carry a Build SBOM name (artifact-first inline,
	// container-first via sbom-cargo.yml at publish stage).
	for _, name := range []string{"cli", "svc"} {
		want := name + "-cargo-build-sbom"
		if got := byName[name].BuildSBOMArtifactName; got != want {
			t.Errorf("%s BuildSBOMArtifactName = %q, want %q", name, got, want)
		}
	}
}

func names(artifacts []pipeline.PlannedArtifact) []string {
	out := make([]string, len(artifacts))
	for i, artifact := range artifacts {
		out[i] = artifact.Name
	}

	return out
}
