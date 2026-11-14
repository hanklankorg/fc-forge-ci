// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
)

func TestNewReleasePlan_ComputesPolicyAndStagePlans(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Sign: config.SignConfig{Method: domainrelease.SignMethodSigstore, Transparency: domainrelease.TransparencyPublic},
		Artifacts: []config.Artifact{
			{
				Name:                 "lib",
				ProjectType:          projecttype.Maven,
				BuildType:            config.BuildTypeLibrary,
				PublishTo:            []config.PublishTarget{config.PublishMavenCentral},
				RequireAuthorization: true,
			},
			{Name: "web", ProjectType: projecttype.NPM, PublishTo: []config.PublishTarget{config.PublishForgePackages}, NPM: &config.NPMConfig{Tag: "release-{version}"}},
			{Name: "rust-service", ProjectType: projecttype.Cargo, Cargo: &config.CargoConfig{BuildMode: config.CargoBuildModeContainerFirst}},
			{Name: "go-service", ProjectType: projecttype.Go, Go: &config.GoConfig{BuildMode: config.GoBuildModeContainerFirst}},
		},
		Containers: []config.Container{{Name: "image"}},
	}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
		ConfigPlan:           pipeline.NewConfigPlan(cfg),
		Branch:               "main",
		RefName:              "v1.2.3",
		ReleasePublisher:     "github-cli",
		ReleaseSBOMs:         "build,analyzed-artifact",
		ReleaseSignArtifacts: true,
		ChangelogCreator:     "git-cliff",
	})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Version != pipeline.ReleasePlanVersion {
		t.Errorf("version = %d", plan.Version)
	}

	if !plan.Policy.MakeLatest || !plan.Policy.CreateRelease || !plan.Policy.SignArtifacts {
		t.Errorf("policy = %+v", plan.Policy)
	}

	if !plan.Policy.RequireAllowlistedSigner || !plan.Policy.RunVersionBump || !plan.Policy.HasContainers {
		t.Errorf("policy = %+v", plan.Policy)
	}

	if plan.Policy.SBOMs != "build,analyzed-artifact" {
		t.Errorf("sboms = %q", plan.Policy.SBOMs)
	}

	if !plan.Stages.Prepare.Targets.VersionBump.Runs {
		t.Errorf("prepare = %+v", plan.Stages.Prepare)
	}

	if !plan.Stages.Build.Targets.Maven.Runs || plan.Stages.Build.Targets.Go.Runs {
		t.Errorf("build = %+v", plan.Stages.Build.Targets)
	}

	if !plan.Stages.Publish.Targets.MavenCentral.Runs || !plan.Stages.Publish.Targets.ForgePackages.Runs {
		t.Errorf("publish package targets = %+v", plan.Stages.Publish.Targets)
	}

	if !plan.Stages.Publish.Targets.Containers.Runs || !plan.Stages.Publish.Targets.CargoContainerFirst.Runs || !plan.Stages.Publish.Targets.GoContainerFirst.Runs {
		t.Errorf("publish container-first targets = %+v", plan.Stages.Publish.Targets)
	}

	if len(plan.ArtifactTransfers.Items) == 0 {
		t.Fatal("artifact transfer plan is empty")
	}

	if !hasTransfer(plan.ArtifactTransfers.Items, "build_artifact", "lib-build-artifacts") {
		t.Errorf("missing maven build transfer: %+v", plan.ArtifactTransfers.Items)
	}
}

func TestNewReleasePlan_ComputesExplicitContainerTransfers(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Sign:      config.SignConfig{Method: domainrelease.SignMethodSigstore, Transparency: domainrelease.TransparencyPublic},
		Artifacts: []config.Artifact{{Name: "go-service", ProjectType: projecttype.Go, Go: &config.GoConfig{BuildMode: config.GoBuildModeContainerFirst}}},
		Containers: []config.Container{{
			Name:                        "api",
			From:                        []string{"go-service"},
			Platforms:                   "linux/amd64,linux/arm64",
			EnableAnalyzedContainerSBOM: true,
			Extract:                     &config.ContainerExtract{Binary: &config.ContainerExtractBinary{Target: "export"}},
		}},
	}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
		ConfigPlan:           pipeline.NewConfigPlan(cfg),
		RefName:              "v1.2.3",
		ReleaseSBOMs:         "analyzed-container",
		ReleaseSignArtifacts: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !hasTransferTemplate(plan.ArtifactTransfers.Items, "analyzed_container_sbom", "analyzed-container-sbom-{run_id}-api-amd64") {
		t.Errorf("missing analyzed container transfer: %+v", plan.ArtifactTransfers.Items)
	}

	if !hasTransfer(plan.ArtifactTransfers.Items, "extracted_binaries", "api-binaries-arm64") {
		t.Errorf("missing binary transfer: %+v", plan.ArtifactTransfers.Items)
	}
}

// A run narrowed by RELEASE_PLATFORMS builds only the selected platforms, so
// the publish stage must not wait for another platform's artifacts: a GitHub
// amd64-only release of a container declaring arm64 failed at publish when the
// plan still required the arm64 analyzed SBOM.
func TestNewReleasePlan_ReleasePlatformsNarrowContainerTransfers(t *testing.T) {
	t.Parallel()

	newPlan := func(selection string) (pipeline.ReleasePlan, error) {
		cfg := &config.Config{
			Sign:      config.SignConfig{Method: domainrelease.SignMethodSigstore, Transparency: domainrelease.TransparencyPublic},
			Artifacts: []config.Artifact{{Name: "go-service", ProjectType: projecttype.Go, Go: &config.GoConfig{BuildMode: config.GoBuildModeContainerFirst}}},
			Containers: []config.Container{{
				Name:                        "api",
				From:                        []string{"go-service"},
				Platforms:                   "linux/amd64,linux/arm64",
				EnableAnalyzedContainerSBOM: true,
				Extract:                     &config.ContainerExtract{Binary: &config.ContainerExtractBinary{Target: "export"}},
			}},
		}
		if err := config.Derive(cfg); err != nil {
			t.Fatal(err)
		}

		return pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
			ConfigPlan:           pipeline.NewConfigPlan(cfg),
			RefName:              "v1.2.3",
			ReleaseSBOMs:         "analyzed-container",
			ReleaseSignArtifacts: true,
			ReleasePlatforms:     selection,
		})
	}

	plan, err := newPlan("linux/amd64")
	if err != nil {
		t.Fatal(err)
	}

	if !hasTransferTemplate(plan.ArtifactTransfers.Items, "analyzed_container_sbom", "analyzed-container-sbom-{run_id}-api-amd64") ||
		!hasTransfer(plan.ArtifactTransfers.Items, "extracted_binaries", "api-binaries-amd64") {
		t.Errorf("selected platform transfers missing: %+v", plan.ArtifactTransfers.Items)
	}

	if hasTransferTemplate(plan.ArtifactTransfers.Items, "analyzed_container_sbom", "analyzed-container-sbom-{run_id}-api-arm64") ||
		hasTransfer(plan.ArtifactTransfers.Items, "extracted_binaries", "api-binaries-arm64") {
		t.Errorf("unselected platform still expected: %+v", plan.ArtifactTransfers.Items)
	}

	if got := plan.Stages.Publish.Targets.Containers.Items[0].Platforms; got != "linux/amd64" {
		t.Errorf("publish container platforms = %q, want linux/amd64", got)
	}

	for _, refused := range []string{"linux/s390x", "linux/amd64,linux/amd64", " linux/amd64"} {
		if _, err := newPlan(refused); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("selection %q: err = %v, want usage refusal", refused, err)
		}
	}
}

func TestNewReleasePlan_RecordsSBOMConflict(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Artifacts: []config.Artifact{{Name: "app", ProjectType: projecttype.NPM, SBOMs: "analyzed-container"}}}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
		ConfigPlan:   pipeline.NewConfigPlan(cfg),
		RefName:      "v1.2.3",
		ReleaseSBOMs: "build",
	})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Policy.SBOMs != "none" {
		t.Errorf("sboms = %q", plan.Policy.SBOMs)
	}

	if plan.Policy.SBOMConflict == nil || plan.Policy.SBOMConflict.ReleaseSBOMs != "build" {
		t.Errorf("sbom conflict = %+v", plan.Policy.SBOMConflict)
	}
}

func TestNewReleasePlan_DoesNotRunCargoOrGoSBOMTargetsWhenBuildSBOMNotEffective(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Artifacts: []config.Artifact{
		{Name: "rust-service", ProjectType: projecttype.Cargo, Cargo: &config.CargoConfig{BuildMode: config.CargoBuildModeContainerFirst}},
		{Name: "go-service", ProjectType: projecttype.Go, Go: &config.GoConfig{BuildMode: config.GoBuildModeContainerFirst}},
	}}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
		ConfigPlan:   pipeline.NewConfigPlan(cfg),
		RefName:      "v1.2.3",
		ReleaseSBOMs: "analyzed-artifact",
	})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Stages.Publish.Targets.CargoContainerFirst.Runs || plan.Stages.Publish.Targets.GoContainerFirst.Runs {
		t.Errorf("build SBOM targets should not run: %+v", plan.Stages.Publish.Targets)
	}
}

func TestNewReleasePlan_FiltersCargoAndGoSBOMTargetsByArtifactSBOMPolicy(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Artifacts: []config.Artifact{
		{Name: "rust-build", ProjectType: projecttype.Cargo, SBOMs: "build", Cargo: &config.CargoConfig{BuildMode: config.CargoBuildModeContainerFirst}},
		{Name: "rust-none", ProjectType: projecttype.Cargo, SBOMs: "none", Cargo: &config.CargoConfig{BuildMode: config.CargoBuildModeContainerFirst}},
		{Name: "go-build", ProjectType: projecttype.Go, SBOMs: "build", Go: &config.GoConfig{BuildMode: config.GoBuildModeContainerFirst}},
		{Name: "go-none", ProjectType: projecttype.Go, SBOMs: "none", Go: &config.GoConfig{BuildMode: config.GoBuildModeContainerFirst}},
	}}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
		ConfigPlan:   pipeline.NewConfigPlan(cfg),
		RefName:      "v1.2.3",
		ReleaseSBOMs: "build",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := names(plan.Stages.Publish.Targets.CargoContainerFirst.Items); len(got) != 1 || got[0] != "rust-build" {
		t.Errorf("cargo items = %v", got)
	}

	if got := names(plan.Stages.Publish.Targets.GoContainerFirst.Items); len(got) != 1 || got[0] != "go-build" {
		t.Errorf("go items = %v", got)
	}
}

func TestNewReleasePlan_RejectsUnsupportedConfigPlanVersion(t *testing.T) {
	t.Parallel()

	_, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
		ConfigPlan: pipeline.ConfigPlan{Version: pipeline.ConfigPlanVersion + 1},
	})
	// A plan from a different build of this tool is skew, so it exits
	// EX_CONFIG rather than blaming the operator's command line.
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}

	if !strings.Contains(err.Error(), "unsupported config-plan version") {
		t.Errorf("err = %v, want it to name the version problem", err)
	}
}

func TestNewReleasePlan_RejectsPushedSLSAWithoutCosignSigner(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		method        domainrelease.SignMethod
		signArtifacts bool
	}{
		{name: "gpg backend", method: domainrelease.SignMethodGPG, signArtifacts: true},
		{name: "signing policy disabled", method: domainrelease.SignMethodSigstore, signArtifacts: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{
				Sign:       config.SignConfig{Method: tc.method},
				Artifacts:  []config.Artifact{{Name: "app", ProjectType: projecttype.NPM}},
				Containers: []config.Container{{Name: "image"}},
			}
			if err := config.Derive(cfg); err != nil {
				t.Fatal(err)
			}

			_, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
				ConfigPlan:           pipeline.NewConfigPlan(cfg),
				ReleaseSBOMs:         "none",
				ReleaseSignArtifacts: tc.signArtifacts,
			})
			if !errors.Is(err, errs.ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}

			// The message names the container and both escape hatches, so the
			// operator can act without reading the source.
			for _, want := range []string{"enables pushed SLSA provenance", "sign.method sigstore or kms", "enable-slsa: false"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to mention %q", err, want)
				}
			}
		})
	}
}

// A declaration may sign its release files with gpg while a cosign method
// signs its images and their provenance. Every release plans it: the GitHub
// and Forgejo releases sign the files with GPG, and the GitLab release warns
// that it signs them with its Cosign key.
func TestReleasePlans_GPGFilesBesideCosignImages(t *testing.T) {
	t.Parallel()

	for _, filesMethod := range []domainrelease.SignMethod{"", domainrelease.SignMethodGPG} {
		t.Run("files-method="+string(filesMethod), func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{
				Sign:       config.SignConfig{Method: domainrelease.SignMethodSigstore, FilesMethod: filesMethod},
				Artifacts:  []config.Artifact{{Name: "app", ProjectType: projecttype.NPM}},
				Containers: []config.Container{{Name: "image"}},
			}
			if err := config.Derive(cfg); err != nil {
				t.Fatal(err)
			}

			configPlan := pipeline.NewConfigPlan(cfg)

			// Pushed SLSA provenance is on by default and needs the cosign method.
			if _, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
				ConfigPlan: configPlan, ReleaseSBOMs: "none", ReleaseSignArtifacts: true, RepositoryVisibility: "public",
			}); err != nil {
				t.Fatalf("NewReleasePlan() error = %v", err)
			}

			if _, _, err := pipeline.NewPolicyReleaseStagePlans(configPlan); err != nil {
				t.Fatalf("NewPolicyReleaseStagePlans() error = %v", err)
			}

			if got := cfg.Sign.DeclaresGPG(); got != (filesMethod != "") {
				t.Errorf("DeclaresGPG() = %t with files-method %q", got, filesMethod)
			}
		})
	}
}

// A release that signs its files with fixed methods warns about a written
// sign.method or sign.files-method outside them and a sign.key other than its
// Cosign key, naming the key it signs with, and about a written
// sign.transparency, which its own input decides. A declaration without a
// sign block, and a release that signs by the declaration, have no warning.
func TestReleaseSigningWarnings(t *testing.T) {
	t.Parallel()

	const (
		otherKey     = "awskms:///alias/release"
		cosignKey    = "the Cosign key (RELEASE_COSIGN_PRIVATE_KEY)"
		policyDecide = "its transparency-log input decides"
	)

	kms := config.SignConfig{Method: domainrelease.SignMethodKMS, Key: config.CosignKeyEnvRef}
	kmsNone := kms
	kmsNone.Transparency = domainrelease.TransparencyNone
	sigstorePublic := config.SignConfig{Method: domainrelease.SignMethodSigstore, Transparency: domainrelease.TransparencyPublic}

	for _, tc := range []struct {
		forge provider.ForgeAPI
		sign  config.SignConfig
		want  []string
	}{
		{forge: provider.ForgeForgejo, sign: config.SignConfig{Method: domainrelease.SignMethodSigstore}, want: []string{"sign.method sigstore is not available on the forgejo release", cosignKey}},
		{forge: provider.ForgeForgejo, sign: config.SignConfig{Method: domainrelease.SignMethodKMS, Key: otherKey}, want: []string{`sign.key "` + otherKey + `" is not used by the forgejo release`, cosignKey}},
		{forge: provider.ForgeForgejo, sign: kms},
		{forge: provider.ForgeForgejo, sign: config.SignConfig{Method: domainrelease.SignMethodGPG}},
		{forge: provider.ForgeForgejo},
		{forge: provider.ForgeGitLab, sign: config.SignConfig{Method: domainrelease.SignMethodSigstore}, want: []string{"sign.method sigstore is not available on the gitlab release", cosignKey}},
		{forge: provider.ForgeGitLab, sign: config.SignConfig{Method: domainrelease.SignMethodGPG}},
		{forge: provider.ForgeGitLab, sign: config.SignConfig{Method: domainrelease.SignMethodKMS, Key: config.CosignKeyEnvRef, FilesMethod: domainrelease.SignMethodGPG}},
		{forge: provider.ForgeGitLab, sign: config.SignConfig{Method: domainrelease.SignMethodSigstore, FilesMethod: domainrelease.SignMethodGPG}, want: []string{"sign.method sigstore is not available on the gitlab release; release files are signed with GPG", cosignKey}},
		{forge: provider.ForgeForgejo, sign: config.SignConfig{Method: domainrelease.SignMethodKMS, Key: config.CosignKeyEnvRef, FilesMethod: domainrelease.SignMethodGPG}},
		{forge: provider.ForgeForgejo, sign: config.SignConfig{Method: domainrelease.SignMethodSigstore, FilesMethod: domainrelease.SignMethodGPG}, want: []string{"sign.method sigstore is not available on the forgejo release; release files are signed with GPG", cosignKey}},
		{forge: provider.ForgeForgejo, sign: config.SignConfig{Method: domainrelease.SignMethodKMS, Key: otherKey, FilesMethod: domainrelease.SignMethodGPG}, want: []string{"release files are signed with GPG", cosignKey}},
		{forge: provider.ForgeGitLab, sign: config.SignConfig{Method: domainrelease.SignMethodKMS, Key: otherKey}, want: []string{`sign.key "` + otherKey + `" is not used by the gitlab release`, cosignKey}},
		{forge: provider.ForgeGitLab, sign: kms},
		{forge: provider.ForgeGitLab},
		{forge: provider.ForgeGitLab, sign: kmsNone, want: []string{"sign.transparency none is not used by the gitlab release", policyDecide}},
		{forge: provider.ForgeForgejo, sign: kmsNone, want: []string{"sign.transparency none is not used by the forgejo release", policyDecide}},
		{forge: provider.ForgeForgejo, sign: sigstorePublic, want: []string{"sign.method sigstore is not available", "sign.transparency public is not used by the forgejo release"}},
		{forge: provider.ForgeGitHub, sign: config.SignConfig{Method: domainrelease.SignMethodSigstore}},
		{forge: provider.ForgeGitHub, sign: config.SignConfig{Method: domainrelease.SignMethodKMS, Key: otherKey}},
		{forge: provider.ForgeGitHub, sign: sigstorePublic},
	} {
		name := strings.Join([]string{string(tc.forge), string(tc.sign.Method), string(tc.sign.FilesMethod), tc.sign.Key, string(tc.sign.Transparency)}, "/")
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := strings.Join(pipeline.ReleaseSigningWarnings(tc.sign, tc.forge), "\n")
			if len(tc.want) == 0 && got != "" {
				t.Fatalf("ReleaseSigningWarnings() = %q, want none", got)
			}

			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("ReleaseSigningWarnings() = %q, want %q", got, want)
				}
			}
		})
	}
}

func TestNewReleasePlan_AllowsNonSLSAOrNonContainerContextsWithoutCosignSigner(t *testing.T) {
	t.Parallel()

	disabled := false
	for _, cfg := range []*config.Config{
		{Artifacts: []config.Artifact{{Name: "app", ProjectType: projecttype.NPM}}},
		{
			Artifacts:  []config.Artifact{{Name: "app", ProjectType: projecttype.NPM}},
			Containers: []config.Container{{Name: "image", EnableSLSA: &disabled}},
		},
	} {
		if err := config.Derive(cfg); err != nil {
			t.Fatal(err)
		}

		if _, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{ConfigPlan: pipeline.NewConfigPlan(cfg), ReleaseSBOMs: "none"}); err != nil {
			t.Fatalf("NewReleasePlan() error = %v", err)
		}
	}
}

func TestNewReleasePlan_RejectsInvalidSBOMInput(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Artifacts: []config.Artifact{{Name: "app", ProjectType: projecttype.NPM}}}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	_, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
		ConfigPlan:   pipeline.NewConfigPlan(cfg),
		ReleaseSBOMs: "bad",
	})
	// A bad --release-sboms value is refused by the domain validator, which
	// classifies it as a rule failure rather than plan skew.
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}

	if !strings.Contains(err.Error(), "sboms: unknown token") {
		t.Errorf("err = %v, want it to name the unknown token", err)
	}
}

func hasTransfer(items []pipeline.ArtifactTransfer, kind, name string) bool {
	for _, item := range items {
		if string(item.Kind) == kind && item.Name == name {
			return true
		}
	}

	return false
}

func hasTransferTemplate(items []pipeline.ArtifactTransfer, kind, tmpl string) bool {
	for _, item := range items {
		if string(item.Kind) == kind && item.NameTemplate == tmpl {
			return true
		}
	}

	return false
}

// TestParseArtifactTransferPlan_RefusesAStaleShape pins the transfer plan to
// the same strict decode as the stage plans. It names the files a download
// lands on disk, so an unknown or repeated member is a producer and consumer
// that disagree about the shape, never something to read past: a misspelt
// "required" decoded as false and made a mandatory artifact optional.
func TestParseArtifactTransferPlan_RefusesAStaleShape(t *testing.T) {
	t.Parallel()

	const item = `{"kind":"extracted_binaries","name":"api-binaries","path":"dist","required":true`

	valid := `{"version":1,"items":[` + item + `}]}`
	if _, err := pipeline.ParseArtifactTransferPlan(valid); err != nil {
		t.Fatalf("control plan refused: %v", err)
	}

	for name, raw := range map[string]string{
		"unknown member":  `{"version":1,"items":[` + item + `,"optional":false}]}`,
		"trailing value":  valid + `{}`,
		"missing version": `{"items":[` + item + `}]}`,
	} {
		if _, err := pipeline.ParseArtifactTransferPlan(raw); !errors.Is(err, errs.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// TestNewReleasePlan_VersionBumpTargetNeedsAnArtifact: the bump policy can be
// on (git-cliff, not skipped) for a configuration that declares no artifacts,
// and then there is nothing whose manifest a bump could rewrite. The target
// must not run on an empty item list.
func TestNewReleasePlan_VersionBumpTargetNeedsAnArtifact(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	plan, err := pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
		ConfigPlan: pipeline.NewConfigPlan(cfg), RefName: "v1.2.3", ChangelogCreator: "git-cliff", ReleaseSBOMs: "none",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !plan.Policy.RunVersionBump {
		t.Fatalf("control: the bump policy is off (%+v)", plan.Policy)
	}

	if target := plan.Stages.Prepare.Targets.VersionBump; target.Runs || len(target.Items) != 0 {
		t.Errorf("version bump target = %+v, want not running with no artifacts", target)
	}
}

// The plan is refused when a platform it builds has no runner, after
// RELEASE_PLATFORMS narrowed it, and when its Xcode jobs have no macOS runner.
func TestNewReleasePlan_RefusesAPlatformWithoutARunner(t *testing.T) {
	t.Parallel()

	newPlan := func(t *testing.T, selection string, xcode bool, pairs ...string) error {
		t.Helper()

		cfg := &config.Config{
			Sign:       config.SignConfig{Method: domainrelease.SignMethodSigstore, Transparency: domainrelease.TransparencyPublic},
			Artifacts:  []config.Artifact{{Name: "go-service", ProjectType: projecttype.Go, Go: &config.GoConfig{BuildMode: config.GoBuildModeContainerFirst}}},
			Containers: []config.Container{{Name: "api", From: []string{"go-service"}, Platforms: "linux/amd64,linux/arm64"}},
		}

		if xcode {
			no := false
			cfg.Artifacts = append(cfg.Artifacts, config.Artifact{Name: "ios", ProjectType: projecttype.XcodeIOS, XcodeIOS: &config.XcodeIOSConfig{EnableCodeSigning: &no}})
		}

		if err := config.Derive(cfg); err != nil {
			t.Fatal(err)
		}

		inputs, err := runners.ParseInputs(provider.ForgeForgejo, pairs)
		if err != nil {
			t.Fatal(err)
		}

		_, err = pipeline.NewReleasePlan(pipeline.ReleasePlanInput{
			ConfigPlan:           pipeline.NewConfigPlan(cfg),
			RefName:              "v1.2.3",
			ReleaseSBOMs:         "none",
			ReleaseSignArtifacts: true,
			ReleasePlatforms:     selection,
			Runners:              inputs,
		})

		return err
	}

	amd64 := `build-runs-on-linux-amd64=["pool"]`

	if err := newPlan(t, "", false, amd64); !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "build-runs-on-linux-arm64") {
		t.Errorf("arm64 build without its runner: err = %v, want a refusal naming build-runs-on-linux-arm64", err)
	}

	if err := newPlan(t, "linux/amd64", false, amd64); err != nil {
		t.Errorf("a run narrowed to amd64 needs no arm64 runner: %v", err)
	}

	if err := newPlan(t, "", false, amd64, `build-runs-on-linux-arm64=["arm64"]`); err != nil {
		t.Errorf("both runners passed: %v", err)
	}

	if err := newPlan(t, "linux/amd64", true, amd64); !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "runs-on-darwin-arm64") {
		t.Errorf("Xcode jobs on Forgejo: err = %v, want a refusal naming runs-on-darwin-arm64", err)
	}

	if err := newPlan(t, "", true); err != nil {
		t.Errorf("a layer that passes no runner inputs is not checked: %v", err)
	}
}
