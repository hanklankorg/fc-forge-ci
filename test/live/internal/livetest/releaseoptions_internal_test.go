// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// A release is held to its option alone: a draft only with the draft option,
// a prerelease only with the prerelease option, and to nothing without one.
func TestReleaseOptionFindings(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		option            string
		draft, prerelease bool
		findings          int
	}{
		"no option, a draft":           {draft: true},
		"draft kept":                   {option: OptionDraft, draft: true},
		"draft published":              {option: OptionDraft, findings: 1},
		"prerelease recorded":          {option: OptionPrerelease, prerelease: true},
		"prerelease not recorded":      {option: OptionPrerelease, findings: 1},
		"prerelease left a draft":      {option: OptionPrerelease, draft: true, prerelease: true, findings: 1},
		"retention published":          {option: OptionRetention},
		"retention marked prerelease":  {option: OptionRetention, prerelease: true, findings: 1},
		"draft marked prerelease, too": {option: OptionDraft, draft: true, prerelease: true, findings: 1},
	} {
		if got := ReleaseOptionFindings(test.option, test.draft, test.prerelease); len(got) != test.findings {
			t.Errorf("%s: %v, want %d findings", name, got, test.findings)
		}
	}
}

// Retention leaves an older release its checksums, signatures and SBOMs,
// removes something else, and leaves the newest release whole.
func TestReleaseRetentionFindings(t *testing.T) {
	t.Parallel()

	before := []string{"app-linux-amd64", "app-linux-arm64", "app-linux-amd64.bundle", "app.cdx.json", "checksums.txt", "checksums.txt.bundle", "slsa-provenance.intoto.json"}
	kept := []string{"app-linux-amd64.bundle", "app.cdx.json", "checksums.txt", "checksums.txt.bundle", "slsa-provenance.intoto.json"}

	for name, test := range map[string]struct {
		after, newest []string
		findings      int
	}{
		"trimmed":                 {after: kept, newest: before},
		"nothing removed":         {after: before, newest: before, findings: 2},
		"one of two left":         {after: append(slices.Clone(kept), "app-linux-arm64"), newest: before, findings: 1},
		"the SBOM removed":        {after: slices.DeleteFunc(slices.Clone(kept), func(n string) bool { return n == "app.cdx.json" }), newest: before, findings: 1},
		"a signature removed":     {after: []string{"app.cdx.json", "checksums.txt", "checksums.txt.bundle", "slsa-provenance.intoto.json"}, newest: before, findings: 1},
		"a file added":            {after: append(slices.Clone(kept), "late.txt"), newest: before, findings: 1},
		"the newest trimmed, too": {after: kept, newest: kept, findings: 1},
	} {
		if got := ReleaseRetentionFindings(before, test.after, test.newest); len(got) != test.findings {
			t.Errorf("%s: %v, want %d findings", name, got, test.findings)
		}
	}

	// The native inventories and the release plan are kept too: losing
	// both is two findings, and a release of kept files alone shows nothing.
	evidence := append(slices.Clone(kept), "sbom-app-amd64.json", "release-plan.json")
	if got := ReleaseRetentionFindings(evidence, kept, before); len(got) != 3 {
		t.Errorf("a release of evidence alone: %v, want 3 findings", got)
	}
}

// A release option is a release trial's, named once; a prerelease version
// and the prerelease option come together; retention needs a second release.
func TestParseTrialSettings_ReleaseOption(t *testing.T) {
	t.Parallel()

	for name, words := range map[string][]string{
		"draft":      {"release-option=draft"},
		"prerelease": {"release-option=prerelease", "version=1.2.3-rc.1"},
		"retention":  {"version=1.2.3", "next-version=1.2.4", "release-option=retention"},
	} {
		if settings, err := ParseTrialSettings(TrialRelease, words); err != nil || settings.ReleaseOption != name {
			t.Errorf("%s: %+v, %v", name, settings, err)
		}
	}

	for name, test := range map[string]struct {
		event string
		words []string
	}{
		"unknown option":                {TrialRelease, []string{"release-option=nightly"}},
		"option twice":                  {TrialRelease, []string{"release-option=draft", "release-option=draft"}},
		"option of a pull request":      {TrialPullRequest, []string{"release-option=draft"}},
		"prerelease, stable version":    {TrialRelease, []string{"release-option=prerelease", "version=1.2.3"}},
		"draft, prerelease version":     {TrialRelease, []string{"release-option=draft", "version=1.2.3-rc.1"}},
		"prerelease with build":         {TrialRelease, []string{"release-option=prerelease", "version=1.2.3-rc.1+b"}},
		"retention of one release":      {TrialRelease, []string{"release-option=retention"}},
		"prerelease as second version":  {TrialRelease, []string{"version=1.2.3", "next-version=1.2.4-rc.1"}},
		"lifecycle, prerelease version": {TrialLifecycle, []string{"CI_JOB_IMAGE=" + snapshotJobImage, "version=1.2.3-rc.1"}},
	} {
		if settings, err := ParseTrialSettings(test.event, test.words); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%s: %+v, %v; want a validation refusal", name, settings, err)
		}
	}
}

// GitLab takes the options its release component has an input for, and no
// draft; a lifecycle takes none.
func TestParseGitLabReleaseSettings_ReleaseOption(t *testing.T) {
	t.Parallel()

	settings, err := ParseGitLabReleaseSettings("version=1.2.3-rc.1\nrelease-option=prerelease")
	if err != nil || settings.Version != "1.2.3-rc.1" || settings.ReleaseOption != OptionPrerelease {
		t.Errorf("a prerelease: %+v, %v", settings, err)
	}

	if inputs := GitLabReleaseOptionInputs(OptionPrerelease); len(inputs) != 1 || inputs["release-policy"] != "stable-prerelease" {
		t.Errorf("the prerelease inputs: %v", inputs)
	}

	if inputs := GitLabReleaseOptionInputs(OptionRetention); len(inputs) != 1 || inputs["release-asset-retention"] != "1" {
		t.Errorf("the retention inputs: %v", inputs)
	}

	if inputs := GitLabReleaseOptionInputs(""); inputs != nil {
		t.Errorf("inputs without an option: %v", inputs)
	}

	for name, input := range map[string]string{
		"draft":                      "version=1.2.3\nrelease-option=draft",
		"option twice":               "version=1.2.3\nnext-version=1.2.4\nrelease-option=retention\nrelease-option=retention",
		"prerelease, stable version": "version=1.2.3\nrelease-option=prerelease",
		"prerelease version alone":   "version=1.2.3-rc.1",
		"retention of one release":   "version=1.2.3\nrelease-option=retention",
	} {
		if _, err := ParseGitLabReleaseSettings(input); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%s: %v, want a validation refusal", name, err)
		}
	}

	if _, err := ParseGitLabLifecycleSettings("version=1.2.3-rc.1\nrelease-option=prerelease"); !errors.Is(err, errs.ErrValidation) {
		t.Errorf("a lifecycle with an option: %v", err)
	}
}

// An option case is named by its option and hands its trial the option
// alone. On Forgejo the trial gives it as one repository variable, which the
// case's demo must read in its release workflow.
func TestPreparedOptionCases(t *testing.T) {
	t.Parallel()

	c := PreparedCase{Lane: LaneReleaseVariants, Demo: "hello-cargo-container", Event: TrialRelease, JobImage: flavorRust, Builder: ImageBuilderBuildah, Version: "0.2.0", Option: OptionDraft}
	if c.Name() != "release-variants/hello-cargo-container/release/buildah/draft" {
		t.Errorf("name %s", c.Name())
	}

	if got, err := c.Settings(preparedImages); err != nil || !slices.Equal(got[2:], []string{"version=0.2.0", "release-option=draft"}) {
		t.Errorf("settings %q, %v", got, err)
	}

	if got := ForgejoReleaseOptionVariables(""); got != nil {
		t.Errorf("variables without an option: %v", got)
	}

	gitlab := PreparedCase{Event: TrialRelease, Version: "0.2.0", NextVersion: "0.2.1", Option: OptionRetention}
	if got := gitlab.GitLabSettings(); !slices.Equal(got, []string{"version=0.2.0", "job-user=65532:65532", "next-version=0.2.1", "release-option=retention"}) {
		t.Errorf("GitLab settings %q", got)
	}

	options := map[string][]string{}

	for _, forge := range []string{forgeForgejo, forgeGitLab} {
		for _, trialCase := range PreparedCases(forge) {
			if trialCase.Option == "" {
				continue
			}

			options[forge] = append(options[forge], trialCase.Option)

			if forge != forgeForgejo {
				continue
			}

			variables := ForgejoReleaseOptionVariables(trialCase.Option)
			if len(variables) != 1 {
				t.Errorf("%s: the option is given as %v, want one variable", trialCase.Name(), variables)
			}

			workflow, err := os.ReadFile(filepath.Join(demoConsumerDir(reporoot.Path(t), trialCase.Demo), filepath.FromSlash(consumerWorkflows), trialReleaseWorkflow))
			for variable := range variables {
				if err != nil || !strings.Contains(string(workflow), "vars."+variable+" ") {
					t.Errorf("%s: the demo's release workflow reads no vars.%s: %v", trialCase.Name(), variable, err)
				}
			}
		}
	}

	// One case per option a forge's release takes; GitLab has no draft.
	slices.Sort(options[forgeForgejo])

	if want := []string{OptionDraft, OptionPrerelease, OptionRetention}; !slices.Equal(options[forgeForgejo], want) || !slices.Equal(options[forgeGitLab], want[1:]) {
		t.Errorf("option cases %v", options)
	}
}

// A later release reuses each base stage when the promotion says what the
// product prints on finding the stage's final tags promoted and verified,
// and hands on the earlier release's projections; a stage signed again, or
// another digest handed on, is not reuse.
func TestBaseStageReuseFindings(t *testing.T) {
	t.Parallel()

	script, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".gitlab/bin/release.sh"))
	if err != nil || !strings.Contains(string(script), `echo "base stage ${stage}: already promoted and verified; reused."`) {
		t.Fatalf("the release no longer says a stage was reused as the oracle reads it: %v", err)
	}

	stages, first := []string{"base", "tools+debug"}, []string{"sha256:1 a", "sha256:2 b", "sha256:3 c"}
	reused := map[string]string{
		"base":        "base stage base: already promoted and verified; reused.\nbase base: {}",
		"tools+debug": "base stage tools+debug: already promoted and verified; reused.\n",
	}
	signed := map[string]string{"base": reused["base"], "tools+debug": "base tools: {}"}

	for name, test := range map[string]struct {
		traces       map[string]string
		first, later []string
		findings     int
	}{
		"every stage reused":      {reused, first, []string{"sha256:3 c", "sha256:1 a", "sha256:2 b"}, 0},
		"a stage signed again":    {signed, first, first, 1},
		"another digest":          {reused, first, []string{"sha256:1 a", "sha256:2 b", "sha256:4 c"}, 1},
		"a flavor not handed on":  {reused, first, first[:2], 1},
		"nothing promoted before": {reused, nil, nil, 1},
	} {
		if got := BaseStageReuseFindings(stages, test.traces, test.first, test.later); len(got) != test.findings {
			t.Errorf("%s: %v, want %d findings", name, got, test.findings)
		}
	}
}
