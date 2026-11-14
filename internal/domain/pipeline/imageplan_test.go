// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

func resolvedBasePlan() pipeline.ReleaseImagesPlan {
	repository := "ghcr.io/acme/app-base"
	tag := strings.Repeat("a", 64) + "-core"

	return pipeline.ReleaseImagesPlan{
		Images: []pipeline.ReleaseImage{{Name: "core", ImageRepository: repository,
			CandidateTag: "staging-" + tag, FinalTagName: tag, Archs: "amd64\narm64", Scan: true,
			ImageKind: "base", Role: "base", Flavor: "core", BaseInputID: strings.Repeat("a", 64),
			ProvenanceJSON: `{"build_inputs":"approved"}`}},
		Builds: []pipeline.ReleaseImageBuild{
			{Name: "core", Arch: "amd64", Platform: "linux/amd64", ContainerFile: "Containerfile", Context: ".",
				Target: "runtime-core", BuildArgs: "TOOL_VERSION=1.2.3", Labels: "org.example.content-id=" + strings.Repeat("a", 64),
				ImageRepository: repository, CandidateTag: "staging-" + tag, FinalTagName: tag},
			{Name: "core", Arch: "arm64", Platform: "linux/arm64", ContainerFile: "Containerfile", Context: ".",
				Target: "runtime-core", BuildArgs: "TOOL_VERSION=1.2.3", Labels: "org.example.content-id=" + strings.Repeat("a", 64),
				ImageRepository: repository, CandidateTag: "staging-" + tag, FinalTagName: tag},
		},
	}
}

func TestRegistryInputsRequirePinnedMatchingBuildArguments(t *testing.T) {
	ref := "ghcr.io/acme/private@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, key, argument, input string
		valid                      bool
	}{
		{"pinned", "BASE_IMAGE", ref, ref, true},
		{"mutable", "BASE_IMAGE", "ghcr.io/acme/private:latest", "ghcr.io/acme/private:latest", false},
		{"different input", "BASE_IMAGE", ref, "ghcr.io/other/private@sha256:" + strings.Repeat("b", 64), false},
		{"path traversal", "../BASE", ref, ref, false},
		{"cache collision", "cache", ref, ref, false},
		{"shell key", "$(command)", ref, ref, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := pipeline.ValidateRegistryInputs(pipeline.ReleaseImageBuild{BuildArgs: tc.key + "=" + tc.argument, RegistryInputs: map[string]string{tc.key: tc.input}})
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errs.ErrInvalidConfig)
			}
		})
	}
}

func TestReadImageBuildPlan_PreservesConsumerPolicyWithoutBackendKnowledge(t *testing.T) {
	t.Parallel()

	want := resolvedBasePlan()
	body, err := json.Marshal(want)
	require.NoError(t, err)
	got, err := pipeline.ReadImageBuildPlan(string(body), "ghcr.io/acme/app-base", "v1.2.3")
	require.NoError(t, err)
	require.Equal(t, want, got)
	// A consumer may also pass the ordinary artifacts.yml planner's output.
	ordinary, err := pipeline.PlanReleaseImages(plannedContainers(t, "  - name: app\n"), releaseImagesInput())
	require.NoError(t, err)
	body, err = json.Marshal(ordinary)
	require.NoError(t, err)
	got, err = pipeline.ReadImageBuildPlan(string(body), "ghcr.io/acme/app", "v1.2.3")
	require.NoError(t, err)
	require.Equal(t, ordinary, got)
}

func TestResolvedBuildLabelsCannotReplaceReleaseIdentity(t *testing.T) {
	t.Parallel()

	for _, key := range pipeline.StandardReleaseImageLabels {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			plan := resolvedBasePlan()
			plan.Builds[0].Labels = key + "=another-source"
			body, err := json.Marshal(plan)
			require.NoError(t, err)
			_, err = pipeline.ReadImageBuildPlan(string(body), "ghcr.io/acme/app-base", "v1.2.3")
			require.ErrorIs(t, err, errs.ErrInvalidConfig)
			require.ErrorContains(t, err, "cannot be replaced")
		})
	}
}

func TestReadImageBuildPlan_RefusesIncompleteOrInconsistentPublication(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*pipeline.ReleaseImagesPlan){
		"missing build":                  func(p *pipeline.ReleaseImagesPlan) { p.Builds = p.Builds[:1] },
		"duplicate build":                func(p *pipeline.ReleaseImagesPlan) { p.Builds = append(p.Builds, p.Builds[0]) },
		"duplicate image":                func(p *pipeline.ReleaseImagesPlan) { p.Images = append(p.Images, p.Images[0]) },
		"foreign image":                  func(p *pipeline.ReleaseImagesPlan) { p.Images[0].ImageRepository = "ghcr.io/other/app" },
		"foreign build":                  func(p *pipeline.ReleaseImagesPlan) { p.Builds[0].ImageRepository = "ghcr.io/other/app" },
		"wrong platform":                 func(p *pipeline.ReleaseImagesPlan) { p.Builds[0].Platform = "linux/arm64" },
		"unknown architecture":           func(p *pipeline.ReleaseImagesPlan) { p.Images[0].Archs = "s390x" },
		"duplicate architecture":         func(p *pipeline.ReleaseImagesPlan) { p.Images[0].Archs = "amd64\namd64" },
		"missing architecture":           func(p *pipeline.ReleaseImagesPlan) { p.Images[0].Archs = "" },
		"outside context":                func(p *pipeline.ReleaseImagesPlan) { p.Builds[0].Context = "../engine" },
		"absolute file":                  func(p *pipeline.ReleaseImagesPlan) { p.Builds[0].ContainerFile = "/etc/passwd" },
		"unsafe metadata name":           func(p *pipeline.ReleaseImagesPlan) { p.Images[0].Name = "../engine" },
		"wrong build tag":                func(p *pipeline.ReleaseImagesPlan) { p.Builds[0].CandidateTag = "staging-other" },
		"final destination as candidate": func(p *pipeline.ReleaseImagesPlan) { p.Images[0].CandidateTag = p.Images[0].FinalTagName },
		"empty plan":                     func(p *pipeline.ReleaseImagesPlan) { *p = pipeline.ReleaseImagesPlan{} },
		"invalid base identity":          func(p *pipeline.ReleaseImagesPlan) { p.Images[0].BaseInputID = "not-a-digest" },
		"missing base identity":          func(p *pipeline.ReleaseImagesPlan) { p.Images[0].BaseInputID = "" },
		"missing base flavor":            func(p *pipeline.ReleaseImagesPlan) { p.Images[0].Flavor = "" },
		"release-scoped content tag":     func(p *pipeline.ReleaseImagesPlan) { p.Images[0].ImageKind = "release" },
		"unknown image kind":             func(p *pipeline.ReleaseImagesPlan) { p.Images[0].ImageKind = "unknown" },
		"malformed provenance":           func(p *pipeline.ReleaseImagesPlan) { p.Images[0].ProvenanceJSON = "{" },
		"unsafe derived SBOM flavor":     func(p *pipeline.ReleaseImagesPlan) { p.Images[0].Flavor = "../../../escape" },
		"invalid builder material":       func(p *pipeline.ReleaseImagesPlan) { p.Builds[0].BuilderMaterialID = "not-a-digest" },
		"unbound cache":                  func(p *pipeline.ReleaseImagesPlan) { p.Images[0].CacheTagPrefix = "cache-arch-core" },
		"foreign cache":                  func(p *pipeline.ReleaseImagesPlan) { p.Builds[0].CacheTag = "registry.invalid/other:cache" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			plan := resolvedBasePlan()
			mutate(&plan)
			body, err := json.Marshal(plan)
			require.NoError(t, err)
			_, err = pipeline.ReadImageBuildPlan(string(body), "ghcr.io/acme/app-base", "v1.2.3")
			require.Error(t, err)
		})
	}
}

func TestReadImageBuildPlan_RejectsSharedSBOMDestinations(t *testing.T) {
	t.Parallel()

	plan := resolvedBasePlan()
	second := plan.Images[0]
	second.Name = "other"
	second.FinalTagName = strings.Repeat("b", 64) + "-core"
	second.CandidateTag = "staging-" + second.FinalTagName
	second.BaseInputID = strings.Repeat("b", 64)
	plan.Images = append(plan.Images, second)
	body, err := json.Marshal(plan)
	require.NoError(t, err)
	_, err = pipeline.ReadImageBuildPlan(string(body), "ghcr.io/acme/app-base", "v1.2.3")
	require.ErrorContains(t, err, "conflicting SBOM path")
}

func TestReadImageBuildPlan_RejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	t.Parallel()

	body, err := json.Marshal(resolvedBasePlan())
	require.NoError(t, err)

	for _, value := range []string{
		string(body) + "{}",
		strings.Replace(string(body), `"builds":`, `"unexpected":true,"builds":`, 1),
		strings.Replace(string(body), `"target":`, `"secrets":"TOKEN","target":`, 1),
		strings.Replace(string(body), `"scan":`, `"unexpected-image-field":true,"scan":`, 1),
	} {
		_, err := pipeline.ReadImageBuildPlan(value, "ghcr.io/acme/app-base", "v1.2.3")
		require.Error(t, err)
	}
}

func TestReadImageBuildPlan_ScansByDefaultAndRefusesAnOptOut(t *testing.T) {
	t.Parallel()

	ordinary, err := pipeline.PlanReleaseImages(plannedContainers(t, "  - name: app\n"), releaseImagesInput())
	require.NoError(t, err)
	body, err := json.Marshal(ordinary)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	images, ok := raw["images"].([]any)
	require.True(t, ok)
	require.Len(t, images, 1)
	image, ok := images[0].(map[string]any)
	require.True(t, ok)
	delete(image, "scan")

	body, err = json.Marshal(raw)
	require.NoError(t, err)

	plan, err := pipeline.ReadImageBuildPlan(string(body), "ghcr.io/acme/app", "v1.2.3")
	require.NoError(t, err)
	require.True(t, plan.Images[0].Scan, "omitting the scan choice must not skip the publication gate")

	image["scan"] = false
	body, err = json.Marshal(raw)
	require.NoError(t, err)
	_, err = pipeline.ReadImageBuildPlan(string(body), "ghcr.io/acme/app", "v1.2.3")
	require.ErrorIs(t, err, errs.ErrInvalidConfig, "an explicit scan opt-out must not skip the publication gate")
	require.ErrorContains(t, err, "scan: false is not supported")

	// A base image's opt-out is refused the same way.
	base := resolvedBasePlan()
	base.Images[0].Scan = false
	body, err = json.Marshal(base)
	require.NoError(t, err)
	_, err = pipeline.ReadImageBuildPlan(string(body), "ghcr.io/acme/app-base", "v1.2.3")
	require.ErrorIs(t, err, errs.ErrInvalidConfig)
}

func TestReadImageBuildPlan_NormalizesArchitectureTransport(t *testing.T) {
	t.Parallel()

	plan := resolvedBasePlan()
	plan.Images[0].Archs = " amd64 \t arm64\n"
	body, err := json.Marshal(plan)
	require.NoError(t, err)
	got, err := pipeline.ReadImageBuildPlan(string(body), "ghcr.io/acme/app-base", "v1.2.3")
	require.NoError(t, err)
	require.Equal(t, "amd64\narm64", got.Images[0].Archs)
}

func TestReadImageBuildPlan_RejectsMovingDestinationCollisions(t *testing.T) {
	t.Parallel()

	for _, collision := range []string{"final", "moving"} {
		t.Run(collision, func(t *testing.T) {
			t.Parallel()

			plan := resolvedBasePlan()
			plan.Images[0].Archs = "amd64"
			plan.Builds = plan.Builds[:1]
			second := plan.Images[0]
			second.Name, second.Flavor = "go", "go"
			second.BaseInputID = strings.Repeat("b", 64)
			second.FinalTagName = second.BaseInputID + "-go"
			second.CandidateTag = "staging-" + second.FinalTagName

			plan.Images[0].MovingTagName = second.FinalTagName
			if collision == "moving" {
				plan.Images[0].MovingTagName = "stable"
				second.MovingTagName = "stable"
			}

			build := plan.Builds[0]
			build.Name, build.CandidateTag, build.FinalTagName = second.Name, second.CandidateTag, second.FinalTagName
			plan.Builds = append(plan.Builds, build)
			plan.Images = append(plan.Images, second)
			body, err := json.Marshal(plan)
			require.NoError(t, err)
			_, err = pipeline.ReadImageBuildPlan(string(body), "ghcr.io/acme/app-base", "v1.2.3")
			require.Error(t, err)
			require.ErrorContains(t, err, "conflicting tag")
		})
	}
}
