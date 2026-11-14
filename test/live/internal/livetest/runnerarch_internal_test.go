// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"runtime"
	"slices"
	"testing"

	"codefloe.com/itiquette/forge-lab-contract/labcontract"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestLabRunnerArch_IsTheOneArchitectureTheRunnersPublish(t *testing.T) {
	t.Parallel()

	runner := func(kind, arch string) labcontract.Endpoint {
		return labcontract.Endpoint{Name: kind, Kind: labcontract.Kind(kind), Capabilities: []string{capabilityWorkflowRuns}, Runners: labcontract.Runners{{Role: labcontract.RoleOrdinary, Platform: "linux/" + arch}}}
	}

	tests := []struct {
		name      string
		endpoints []labcontract.Endpoint
		want      string
		refused   bool
	}{
		{name: "agreeing runners", endpoints: []labcontract.Endpoint{runner("gitlab", "arm64"), runner("forgejo", "arm64")}, want: "arm64"},
		{name: "no runner", endpoints: []labcontract.Endpoint{{Name: "gitlab", Kind: "gitlab"}}, want: runtime.GOARCH},
		{name: "unpublished", endpoints: []labcontract.Endpoint{{Name: "gitlab", Kind: "gitlab", Capabilities: []string{capabilityWorkflowRuns}}}, refused: true},
		{name: "differing runners", endpoints: []labcontract.Endpoint{runner("gitlab", "amd64"), runner("forgejo", "arm64")}, refused: true},
	}

	for _, test := range tests {
		got, err := labRunnerArch(&labcontract.Contract{Endpoints: test.endpoints})
		if test.refused != (err != nil) || got != test.want {
			t.Errorf("%s: labRunnerArch() = %q, %v", test.name, got, err)
		}
	}
}

func TestNativeReleasePlatforms_DefaultsToTheRunnerAndRefusesAnotherArchitecture(t *testing.T) {
	t.Parallel()

	got, err := NativeReleasePlatforms(map[string]string{"OTHER": "kept"}, "arm64")
	if err != nil || got[releasePlatformsVariable] != "linux/arm64" || got["OTHER"] != "kept" {
		t.Fatalf("an unset selection: %v, %v", got, err)
	}

	if got, err = NativeReleasePlatforms(map[string]string{releasePlatformsVariable: "linux/amd64"}, "amd64"); err != nil ||
		got[releasePlatformsVariable] != "linux/amd64" {
		t.Fatalf("a native selection: %v, %v", got, err)
	}

	for _, selection := range []string{"linux/arm64", "linux/amd64,linux/arm64"} {
		if _, err := NativeReleasePlatforms(map[string]string{releasePlatformsVariable: selection}, "amd64"); err == nil {
			t.Errorf("%s was accepted on an amd64 lab", selection)
		}
	}
}

// A lab publishes an ordinary and a build pool. Buildah builds on the build
// pool everywhere. Kaniko needs no user namespaces: on Forgejo it runs on the
// ordinary pool, and on GitLab, whose ordinary pool takes untagged jobs while
// the product asks for a build tag, on the build pool too.
func TestChooseImageBuilder_SchedulesOnTheContractsPools(t *testing.T) {
	t.Setenv(liveProfileEnv, profileFull)
	t.Setenv(KanikoJobImageArchiveEnv, "/prepared/kaniko.tar")

	pools := func(ordinary []string) labcontract.Runners {
		return labcontract.Runners{
			{Role: labcontract.RoleOrdinary, Platform: "linux/amd64", Tags: ordinary},
			{Role: labcontract.RoleBuild, Platform: "linux/amd64", Tags: []string{"privileged"}, Exclusive: true},
		}
	}

	for _, test := range []struct {
		forge   provider.ForgeAPI
		runners labcontract.Runners
		builder string
		want    string
	}{
		{provider.ForgeForgejo, pools([]string{"ubuntu-latest"}), ImageBuilderBuildah, "privileged"},
		{provider.ForgeForgejo, pools([]string{"ubuntu-latest"}), ImageBuilderKaniko, "ubuntu-latest"},
		{provider.ForgeGitLab, pools([]string{}), ImageBuilderBuildah, "privileged"},
		{provider.ForgeGitLab, pools([]string{}), ImageBuilderKaniko, "privileged"},
	} {
		t.Setenv(ImageBuilderEnv(test.forge), test.builder)

		target := Target{accepted: true, Forge: test.forge, endpoint: labcontract.Endpoint{Name: string(test.forge), Runners: test.runners}}
		if got := ChooseImageBuilder(t, target); got.Builder != test.builder || !slices.Equal(got.RunnerLabels, []string{test.want}) {
			t.Errorf("%s with %s builds on %q as %q, want on %q", test.forge, test.builder, got.RunnerLabels, got.Builder, test.want)
		}

		if got := RunnerArch(t, target); got != "amd64" {
			t.Errorf("%s runner architecture = %q", test.forge, got)
		}
	}
}
