// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package runners_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
)

func TestInputNamesFollowTheRolePattern(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		forge    provider.ForgeAPI
		role     runners.Role
		platform string
		want     string
	}{
		{provider.ForgeGitLab, runners.Ordinary, "", "runner-tags"},
		{provider.ForgeGitLab, runners.Build, "linux/arm64", "build-runner-tags-linux-arm64"},
		{provider.ForgeGitLab, runners.Signer, "", "signer-runner-tags"},
		{provider.ForgeGitLab, runners.Ordinary, "darwin/arm64", "runner-tags-darwin-arm64"},
		{provider.ForgeForgejo, runners.Build, "linux/amd64", "build-runs-on-linux-amd64"},
		{provider.ForgeForgejo, runners.Signer, "", "signer-runs-on"},
		{provider.ForgeForgejo, runners.Signer, "linux/amd64", "signer-runs-on-linux-amd64"},
		{provider.ForgeGitHub, runners.Ordinary, "darwin/arm64", "runs-on-darwin-arm64"},
		{provider.ForgeGitHub, runners.Ordinary, "", "runs-on"},
	} {
		got, err := runners.Input(tc.forge, tc.role, tc.platform)
		if err != nil || got != tc.want {
			t.Errorf("Input(%s, %s, %q) = %q, %v; want %q", tc.forge, tc.role, tc.platform, got, err, tc.want)
		}
	}

	for _, refused := range []struct {
		forge    provider.ForgeAPI
		role     runners.Role
		platform string
	}{
		{provider.ForgeLocal, runners.Ordinary, ""},
		{provider.ForgeGitLab, "checks", ""},
		{provider.ForgeGitLab, runners.Build, "linux/s390x"},
		{provider.ForgeGitLab, runners.Build, "linux-arm64"},
		{provider.ForgeGitHub, runners.Signer, ""}, // a hosted literal
	} {
		if _, err := runners.Input(refused.forge, refused.role, refused.platform); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("Input(%s, %s, %q): err = %v, want usage refusal", refused.forge, refused.role, refused.platform, err)
		}
	}
}

func TestParseInputsRefusesWhatNoLayerPasses(t *testing.T) {
	t.Parallel()

	for _, pairs := range [][]string{
		{"build-runs-on-linux-arm64"},                   // not name=value
		{"build-runner-tags-linux-arm64=[]"},            // a GitLab name on Forgejo
		{"runner-label=[\"pool\"]"},                     // an old name
		{"runs-on=[\"a\"]", "runs-on=[\"b\"]"},          // twice
		{"build-runs-on-linux-arm64-extra=[\"arm64\"]"}, // no such platform
	} {
		if _, err := runners.ParseInputs(provider.ForgeForgejo, pairs); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("ParseInputs(forgejo, %q): err = %v, want usage refusal", pairs, err)
		}
	}

	if _, err := runners.ParseInputs(provider.ForgeLocal, []string{"runs-on=[\"a\"]"}); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("a local run took runner inputs: %v", err)
	}

	// GitHub's signer jobs run on a hosted literal; no input chooses theirs.
	for _, name := range []string{"signer-runs-on", "signer-runs-on-linux-amd64"} {
		if _, err := runners.ParseInputs(provider.ForgeGitHub, []string{name + `=["ubuntu-26.04"]`}); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("GitHub took %s: %v", name, err)
		}
	}
}

// Names is every name Input makes for a forge, and only those: what a guard
// holds each layer's runner inputs to.
func TestNames(t *testing.T) {
	t.Parallel()

	gitlab, err := runners.Names(provider.ForgeGitLab)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"runner-tags", "runner-tags-linux-amd64", "runner-tags-linux-arm64", "runner-tags-darwin-arm64",
		"build-runner-tags", "build-runner-tags-linux-amd64", "build-runner-tags-linux-arm64", "build-runner-tags-darwin-arm64",
		"signer-runner-tags", "signer-runner-tags-linux-amd64", "signer-runner-tags-linux-arm64", "signer-runner-tags-darwin-arm64",
	}
	if !slices.Equal(gitlab, want) {
		t.Errorf("Names(gitlab) = %v, want %v", gitlab, want)
	}

	github, err := runners.Names(provider.ForgeGitHub)
	if err != nil {
		t.Fatal(err)
	}

	if len(github) != 8 || slices.ContainsFunc(github, func(name string) bool { return strings.HasPrefix(name, "signer-") }) {
		t.Errorf("Names(github) = %v, want the ordinary and build names only", github)
	}

	if _, err := runners.Names(provider.ForgeLocal); !errors.Is(err, errs.ErrUsage) {
		t.Errorf("Names(local): err = %v, want usage refusal", err)
	}
}

var (
	arm64Builds = runners.Need{Role: runners.Build, Platform: "linux/arm64", Jobs: "image builds"}
	amd64Builds = runners.Need{Role: runners.Build, Platform: "linux/amd64", Jobs: "image builds"}
	xcodeJobs   = runners.Need{Role: runners.Ordinary, Platform: "darwin/arm64", Jobs: "Xcode and App Store jobs"}
	signerJobs  = runners.Need{Role: runners.Signer, Jobs: "signing jobs"}
)

func TestAdmit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		forge   provider.ForgeAPI
		pairs   []string
		needs   []runners.Need
		refused string // the input the refusal names; empty admits
	}{
		// Refused where no value reached the engine, on each forge.
		{"gitlab input not passed", provider.ForgeGitLab, []string{"build-runner-tags-linux-amd64=[\"amd64\"]"}, []runners.Need{arm64Builds}, "build-runner-tags-linux-arm64"},
		{"gitlab input empty", provider.ForgeGitLab, []string{"build-runner-tags-linux-arm64="}, []runners.Need{arm64Builds}, "build-runner-tags-linux-arm64"},
		{"forgejo required input empty", provider.ForgeForgejo, []string{"build-runs-on-linux-amd64="}, []runners.Need{amd64Builds}, "build-runs-on-linux-amd64"},
		{"forgejo empty label list", provider.ForgeForgejo, []string{"build-runs-on-linux-arm64=[]"}, []runners.Need{arm64Builds}, "build-runs-on-linux-arm64"},
		{"github input not passed", provider.ForgeGitHub, []string{"runs-on=[\"ubuntu-26.04\"]"}, []runners.Need{xcodeJobs}, "runs-on-darwin-arm64"},
		// Defaults are values: GitLab's [] is untagged, GitHub's are hosted.
		{"gitlab untagged default", provider.ForgeGitLab, []string{"signer-runner-tags=[]"}, []runners.Need{signerJobs}, ""},
		{"github hosted unbound default", provider.ForgeGitHub, []string{"build-runs-on=[\"ubuntu-26.04\"]"}, []runners.Need{{Role: runners.Build, Jobs: "image builds"}}, ""},
		// An empty list binds no runner on Forgejo, nor any platform-bound
		// input on GitLab: an untagged job could land on any platform.
		{"forgejo unbound empty list", provider.ForgeForgejo, []string{"signer-runs-on=[]"}, []runners.Need{signerJobs}, "signer-runs-on"},
		{"gitlab platform-bound untagged", provider.ForgeGitLab, []string{"build-runner-tags-linux-arm64=[]"}, []runners.Need{arm64Builds}, "build-runner-tags-linux-arm64"},
		{"gitlab ordinary untagged", provider.ForgeGitLab, []string{"runner-tags=[]"}, []runners.Need{{Role: runners.Ordinary, Jobs: "ordinary jobs"}}, ""},
		{"gitlab unbound build untagged", provider.ForgeGitLab, []string{"build-runner-tags=[]"}, []runners.Need{{Role: runners.Build, Jobs: "image builds"}}, "build-runner-tags"},
		{"gitlab untagged darwin", provider.ForgeGitLab, []string{"runner-tags-darwin-arm64=[]"}, []runners.Need{xcodeJobs}, "runner-tags-darwin-arm64"},
		{"gitlab explicit arm64", provider.ForgeGitLab, []string{"build-runner-tags-linux-arm64=[\"arm64\"]"}, []runners.Need{arm64Builds}, ""},
		{"github hosted defaults", provider.ForgeGitHub, []string{"build-runs-on-linux-arm64=[\"ubuntu-26.04-arm\"]", "runs-on-darwin-arm64=[\"macos-26\"]"}, []runners.Need{arm64Builds, xcodeJobs}, ""},
		// Explicit values.
		{"forgejo explicit", provider.ForgeForgejo, []string{"build-runs-on-linux-amd64=[\"pool\"]", "build-runs-on-linux-arm64=[\"arm64\"]"}, []runners.Need{amd64Builds, arm64Builds}, ""},
		{"gitlab explicit macOS", provider.ForgeGitLab, []string{"runner-tags-darwin-arm64=[\"saas-macos-medium-m1\"]"}, []runners.Need{xcodeJobs}, ""},
		// No fallback across platforms or roles.
		{"arm64 does not fall back to amd64", provider.ForgeForgejo, []string{"build-runs-on-linux-amd64=[\"pool\"]"}, []runners.Need{amd64Builds, arm64Builds}, "build-runs-on-linux-arm64"},
		{"build does not fall back to ordinary", provider.ForgeForgejo, []string{"runs-on-linux-arm64=[\"arm64\"]"}, []runners.Need{arm64Builds}, "build-runs-on-linux-arm64"},
		// Forgejo has no macOS runner.
		{"darwin job on forgejo", provider.ForgeForgejo, []string{"runs-on=[\"pool\"]", "build-runs-on-linux-amd64=[\"pool\"]"}, []runners.Need{amd64Builds, xcodeJobs}, "runs-on-darwin-arm64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inputs, err := runners.ParseInputs(tc.forge, tc.pairs)
			if err != nil {
				t.Fatal(err)
			}

			err = inputs.Admit(tc.needs)
			if tc.refused == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}

				return
			}

			if !errors.Is(err, errs.ErrUsage) {
				t.Fatalf("err = %v, want usage refusal", err)
			}

			// The refusal names the missing input, the platform and the forge.
			need := tc.needs[len(tc.needs)-1]
			for _, want := range []string{" " + tc.refused + " ", "set " + tc.refused + " to a JSON list", need.Platform, " " + string(tc.forge) + " "} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not name %q", err, want)
				}
			}
		})
	}
}

// A layer that passes no runner inputs yet is not checked.
func TestAdmitWithoutInputsChecksNothing(t *testing.T) {
	t.Parallel()

	inputs, err := runners.ParseInputs(provider.ForgeForgejo, nil)
	if err != nil || inputs != nil {
		t.Fatalf("ParseInputs(nil) = %v, %v; want nil, nil", inputs, err)
	}

	if err := inputs.Admit([]runners.Need{arm64Builds, xcodeJobs}); err != nil {
		t.Fatal(err)
	}
}
