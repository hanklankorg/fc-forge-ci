// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The demos the Forgejo release scenarios run, as the scenario map names them.
var forgejoFixtureDemos = slices.Compact([]string{
	preparedScenarioDemos["TestForgejoRelease_RejectsPreseededProvenance"],
	preparedScenarioDemos["TestForgejoRelease_InterruptedRecovery"],
	preparedScenarioDemos[forgejoRerunTest],
})

func forgejoDemoRelease(t *testing.T, demo string) (string, map[string]any) {
	t.Helper()

	body := string(reporoot.ReadFile(t, "test/live/fixtures/consumers/"+demo+"/consumer/"+forgejoReleaseWorkflow))

	_, jobs, err := forgejoReleaseJobs(body)
	if err != nil {
		t.Fatal(err)
	}

	return body, jobs
}

// The preseeded file must be in the artifact the signer downloads, written
// after the consumer's own steps, and nothing else of the workflow may change.
func TestForgejoPreseededProvenanceIsTheLastStepBeforeTheHandoffUpload(t *testing.T) {
	for _, demo := range forgejoFixtureDemos {
		t.Run(demo, func(t *testing.T) {
			body, original := forgejoDemoRelease(t, demo)

			rendered, err := forgejoPreseededProvenanceWorkflow(body)
			if err != nil {
				t.Fatal(err)
			}

			_, jobs, err := forgejoReleaseJobs(rendered)
			if err != nil {
				t.Fatal(err)
			}

			preseeded := 0

			for name, value := range jobs {
				job, _ := value.(map[string]any)
				steps, _ := job[fieldSteps].([]any)

				for index, entry := range steps {
					step, _ := entry.(map[string]any)
					if run, _ := step["run"].(string); !strings.Contains(run, `> "${FIXTURE_DIST}/slsa-provenance.intoto.json"`) {
						continue
					}

					preseeded++

					upload, _ := steps[index+1].(map[string]any)
					if uses, _ := upload[fieldUses].(string); !strings.Contains(uses, forgejoUploadDist) || forgejoInput(upload, "artifact-name", forgejoHandoffArtifact) != forgejoHandoffArtifact {
						t.Errorf("job %s: the step after the fixture is %v, want the upload of %s", name, upload, forgejoHandoffArtifact)
					}

					if env, _ := step["env"].(map[string]any); env["FIXTURE_DIST"] != "dist" {
						t.Errorf("job %s: the fixture writes into %v, want dist", name, env["FIXTURE_DIST"])
					}

					job[fieldSteps] = slices.Delete(steps, index, index+1)

					break
				}
			}

			if preseeded != 1 || !reflect.DeepEqual(jobs, original) {
				t.Fatalf("%d fixture steps, or the workflow changed beyond one: %s", preseeded, rendered)
			}
		})
	}
}

// Signing must need the hold, the hold the reached job, and the reached job
// everything signing needed, so the checkpoint is after the whole hand-off.
func TestForgejoRecoveryCheckpointSitsBetweenHandoffAndSigning(t *testing.T) {
	for _, demo := range forgejoFixtureDemos {
		t.Run(demo, func(t *testing.T) {
			body, original := forgejoDemoRelease(t, demo)

			rendered, err := forgejoRecoveryCheckpointWorkflow(body)
			if err != nil {
				t.Fatal(err)
			}

			_, jobs, err := forgejoReleaseJobs(rendered)
			if err != nil {
				t.Fatal(err)
			}

			name, signer, err := forgejoSigningJob(jobs)
			if err != nil {
				t.Fatal(err)
			}

			before, _ := original[name].(map[string]any)

			needed, err := forgejoNeeds(before[fieldNeeds])
			if err != nil {
				t.Fatal(err)
			}

			reached, _ := jobs[forgejoRecoveryReachedJob].(map[string]any)
			hold, _ := jobs[forgejoRecoveryHoldJob].(map[string]any)

			if !reflect.DeepEqual(signer[fieldNeeds], append(slices.Clone(needed), any(forgejoRecoveryHoldJob))) ||
				!reflect.DeepEqual(reached[fieldNeeds], needed) || !reflect.DeepEqual(hold[fieldNeeds], []any{forgejoRecoveryReachedJob}) {
				t.Errorf("signing needs %v, the hold %v and the reached job %v; the signing job needed %v", signer[fieldNeeds], hold[fieldNeeds], reached[fieldNeeds], needed)
			}

			if reached[fieldName] != ForgejoRecoveryCheckpointReached || hold[fieldName] != ForgejoRecoveryCheckpointHold {
				t.Errorf("the checkpoint jobs are named %v and %v", reached[fieldName], hold[fieldName])
			}

			if condition, conditional := before[fieldIf]; conditional {
				want, _ := forgejoHeldCondition(condition)
				if signer[fieldIf] != want {
					t.Errorf("signing runs if %v, want %s", signer[fieldIf], want)
				}
			} else if _, conditional := signer[fieldIf]; conditional {
				t.Errorf("signing gained the condition %v", signer[fieldIf])
			}

			// With the gate taken out again, the workflow is the consumer's.
			delete(jobs, forgejoRecoveryReachedJob)
			delete(jobs, forgejoRecoveryHoldJob)

			signer[fieldNeeds], signer[fieldIf] = before[fieldNeeds], before[fieldIf]

			if _, conditional := before[fieldIf]; !conditional {
				delete(signer, fieldIf)
			}

			if !reflect.DeepEqual(jobs, original) {
				t.Errorf("the workflow changed beyond the gate: %s", rendered)
			}
		})
	}
}

func TestForgejoHeldCondition(t *testing.T) {
	const held = " && needs.fixture-recovery-hold.result == 'success' }}"

	for name, test := range map[string]struct {
		condition any
		want      string
	}{
		"expression":      {condition: "${{ always() && needs.build.result == 'success' }}", want: "${{ (always() && needs.build.result == 'success')" + held},
		"bare expression": {condition: "success() || failure()", want: "${{ (success() || failure())" + held},
		"unclosed":        {condition: "${{ always()"},
		"not a string":    {condition: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := forgejoHeldCondition(test.condition)
			if got != test.want || (test.want == "") != errors.Is(err, errForgejoReleaseFixture) {
				t.Fatalf("got %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestForgejoReleaseFixturesRefuseAWorkflowTheyDoNotFit(t *testing.T) {
	const (
		signer = "  sign:\n    needs: [build]\n    uses: owner/layer/.forgejo/workflows/sign-and-publish-release.yml@0123\n"
		upload = "      - uses: https://forge.example/owner/layer/.forgejo/actions/upload-dist@0123\n"
		build  = "  build:\n    steps:\n" + upload
	)

	for name, test := range map[string]struct {
		workflow            string
		preseed, checkpoint bool
	}{
		"fits":                      {workflow: "jobs:\n" + build + signer, preseed: true, checkpoint: true},
		"no jobs":                   {workflow: "name: Release\n"},
		"no signing job":            {workflow: "jobs:\n" + build},
		"two signing jobs":          {workflow: "jobs:\n" + build + signer + strings.Replace(signer, "sign:", "sign-again:", 1)},
		"no hand-off upload":        {workflow: "jobs:\n  build:\n    steps:\n      - run: make\n" + signer, checkpoint: true},
		"two hand-off uploads":      {workflow: "jobs:\n" + build + upload + signer, checkpoint: true},
		"two hand-off jobs":         {workflow: "jobs:\n" + build + strings.Replace(build, "build:", "handoff:", 1) + signer, checkpoint: true},
		"upload of another name":    {workflow: "jobs:\n" + build + "        with:\n          artifact-name: release-files\n" + signer, checkpoint: true},
		"signer reads another path": {workflow: "jobs:\n" + build + signer + "    with:\n      artifact-path: out/\n", checkpoint: true},
		"signing needs nothing":     {workflow: "jobs:\n" + build + strings.Replace(signer, "    needs: [build]\n", "", 1), preseed: true},
		"gate job name taken":       {workflow: "jobs:\n" + build + signer + "  " + forgejoRecoveryHoldJob + ":\n    steps: []\n", preseed: true},
		"condition not expression":  {workflow: "jobs:\n" + build + signer + "    if: true\n", preseed: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, preseedErr := forgejoPreseededProvenanceWorkflow(test.workflow)
			_, checkpointErr := forgejoRecoveryCheckpointWorkflow(test.workflow)

			if test.preseed != (preseedErr == nil) || test.checkpoint != (checkpointErr == nil) {
				t.Fatalf("preseed: %v; checkpoint: %v", preseedErr, checkpointErr)
			}

			for _, err := range []error{preseedErr, checkpointErr} {
				if err != nil && !errors.Is(err, errForgejoReleaseFixture) {
					t.Fatalf("refused for another reason: %v", err)
				}
			}
		})
	}
}

// An interrupted Forgejo release is recovered with the next patch version,
// and only a plain release version has one.
func TestNextPatchVersion(t *testing.T) {
	t.Parallel()

	for version, want := range map[string]string{"0.0.1": "0.0.2", "1.2.9": "1.2.10", "3.0.0": "3.0.1"} {
		if next, err := NextPatchVersion(version); err != nil || next != want {
			t.Errorf("NextPatchVersion(%q) = %q, %v, want %q", version, next, err, want)
		}
	}

	for _, version := range []string{"", "1.2", "1.2.3.4", "v1.2.3", "1.2.3-rc.1", "1.2.3+build", "1.2.x", "01.2.3"} {
		if next, err := NextPatchVersion(version); !errors.Is(err, errForgejoReleaseFixture) {
			t.Errorf("NextPatchVersion(%q) = %q, %v, want a refusal", version, next, err)
		}
	}
}

// A rerun of the hold runs every job that needs it, directly or through
// another job, and no other.
func TestDependantJobs(t *testing.T) {
	for _, tt := range []struct {
		name  string
		needs map[string][]string
		want  []string
	}{
		{"direct", map[string][]string{"hold": {"reached"}, "sign": {"build", "hold"}, "build": nil}, []string{"sign"}},
		{"transitive", map[string][]string{"sign": {"hold"}, "summary": {"build", "sign"}}, []string{"sign", "summary"}},
		{"unrelated", map[string][]string{"hold": {"build"}, "lint": {"build"}}, []string{}},
		{"diamond", map[string][]string{"left": {"hold"}, "right": {"hold"}, "join": {"left", "right"}, "after": {"join"}}, []string{"after", "join", "left", "right"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := dependantJobs(tt.needs, "hold"); !slices.Equal(got, tt.want) {
				t.Errorf("dependantJobs() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The dependants are named as the run lists them: by name:, or by key.
func TestForgejoRecoveryHoldDependants(t *testing.T) {
	body := `jobs:
  ` + forgejoRecoveryHoldJob + `:
    name: hold
  sign:
    name: Sign
    needs: ` + forgejoRecoveryHoldJob + `
  summary:
    needs: [build, sign]
  build:
    name: Build
`

	got, err := ForgejoRecoveryHoldDependants(body)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"Sign", "summary"}; !slices.Equal(got, want) {
		t.Errorf("ForgejoRecoveryHoldDependants() = %v, want %v", got, want)
	}
}
