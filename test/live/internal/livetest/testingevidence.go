// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
)

// SeedTestingEvidence drafts TestingMatricesEvidence lines from the runs
// recorded under liveRuns, the live runner's <run>/verdict.json. A hosted
// GitHub trial records no verdict there, so its mark is written by hand.
//
// A run is evidence for each case it ran: a prepared case with its
// visibility, or the one consumer of a run that names it, with the axis
// values its verdict header records (evidenceAxes). The seed keeps the
// latest result of each case, so a later run of other cases of a flow
// leaves a case's failure standing, and a rerun of exactly that case
// replaces it. Each demo's flow on each provider is then marked with the
// worst result of its cases (worstCase): passed as ✅, passed on a row's
// rerun as ✅+rerun, failed as ❌, anything else as ⏳, with the date of the
// run that gave it as the note. A lifecycle is marked for each set of the
// axis values its cases ran with, which lead the note.
//
// A delta run of a run set (FORGE_CI_LIVE_AFTER) ran what the run of its set
// after did not, and is evidence only beside that run: an earlier run of the
// set after on the same commit and provider, from the same prepared
// directory and with the same pattern, itself a whole run or a delta run
// with such a run before it (deltaRunsBefore). Its result of a case then
// adds to that run's rather than replacing it, the worse of the two kept
// (keepLatest). A delta run without one is left out, and a comment line at
// the top of the draft names it. It reads the recorded facts and makes no
// judgement: the overlay is hand-kept, so the operator reviews the draft
// before committing it.
func SeedTestingEvidence(liveRuns string) (string, error) {
	latest := map[string]map[string]seededRun{}

	leftOut, err := seedLiveRuns(liveRuns, latest)
	if err != nil {
		return "", err
	}

	lines := make([]string, 0, len(latest))

	for row, cases := range latest {
		run := worstCase(cases)
		// The axis values follow the flow in the row and the mark in the line.
		words := strings.Fields(row)
		lines = append(lines, strings.Join(slices.Concat(words[:3], []string{evidenceMark(run.verdict)}, words[3:], []string{run.date()}), " "))
	}

	slices.Sort(lines)

	return strings.Join(slices.Concat(leftOut, lines), "\n") + "\n", nil
}

// seededRun is one recorded run: when it started or finished, its verdict,
// the commit it ran and whether it was a delta run.
type seededRun struct {
	at      time.Time
	verdict string
	commit  string
	delta   bool
}

func (run seededRun) date() string {
	return run.at.Format(time.DateOnly)
}

// keepLatest records run as the result of a case of an overlay row unless a
// later run of the case is recorded. A later delta run on the commit of the
// recorded one keeps the worse verdict of the two, as the parts of one run.
func keepLatest(latest map[string]map[string]seededRun, row, key string, run seededRun) {
	if row == "" {
		return
	}

	if latest[row] == nil {
		latest[row] = map[string]seededRun{}
	}

	kept, seen := latest[row][key]

	later, earlier := run, kept
	if kept.at.After(run.at) {
		later, earlier = kept, run
	}

	if seen && later.delta && later.commit != "" && later.commit == earlier.commit {
		later.verdict = worseResult(later.verdict, earlier.verdict)
	}

	latest[row][key] = later
}

// worstCase is the kept run of a row's cases with the worst result, the
// latest of those with it.
func worstCase(cases map[string]seededRun) seededRun {
	var worst seededRun

	for _, run := range cases {
		if worst.at.IsZero() || resultRank(run.verdict) < resultRank(worst.verdict) ||
			resultRank(run.verdict) == resultRank(worst.verdict) && run.at.After(worst.at) {
			worst = run
		}
	}

	return worst
}

// worseResult is the worse of two results of a case.
func worseResult(one, other string) string {
	if resultRank(other) < resultRank(one) {
		return other
	}

	return one
}

// resultRank orders the results of a case from the worst: failed, then
// unfinished or any other verdict, then passed on rerun, then passed.
func resultRank(result string) int {
	switch result {
	case liveVerdictFailed:
		return 0
	case seedPassedOnRerun:
		return 2
	case verdictPassed:
		return 3
	default:
		return 1
	}
}

// recordedRun is a verdict.json as read, with the time its run started.
type recordedRun struct {
	file    string
	verdict LiveRunVerdict
	at      time.Time
}

// set and after are the run set the run recorded and the set its delta came
// after, none for a whole run.
func (run recordedRun) set() string {
	return cmp.Or(run.verdict.Run.Inputs[strings.ToLower(liveSetEnv)], SetFull)
}

func (run recordedRun) after() string {
	return run.verdict.Run.Inputs[strings.ToLower(liveAfterEnv)]
}

// preparedEnvHash is the SHA-256 of the prepared.env of the prepared
// directory the run took, none for a run of no prepared directory.
func (run recordedRun) preparedEnvHash() string {
	return run.verdict.Run.Inputs[strings.ToLower(preparedEnvHashEnv)]
}

// deltaRunsBefore reports whether a delta run has the runs it adds to: an
// earlier run of its set after on its commit and provider, from its prepared
// directory and with its pattern, that ran to its end and is a whole run or
// has such runs before it in turn. An interrupted run may have left cases
// unrun, so a delta cannot complete it.
//
// The prepared directory is the hash of its prepared.env the runs record
// (preparedEnvHash), which must be the same: a run that records none, as
// before the hash was recorded, matches only another that records none.
func deltaRunsBefore(runs []recordedRun, delta recordedRun) bool {
	if delta.after() == "" {
		return true
	}

	header := delta.verdict.Run

	return header.SourceCommit != "" && slices.ContainsFunc(runs, func(run recordedRun) bool {
		earlier := run.verdict.Run

		return run.at.Before(delta.at) && run.verdict.Verdict != verdictInterrupted &&
			run.set() == delta.after() && earlier.SourceCommit == header.SourceCommit &&
			earlier.Provider == header.Provider && earlier.Scenario == header.Scenario &&
			run.preparedEnvHash() == delta.preparedEnvHash() && deltaRunsBefore(runs, run)
	})
}

// readLiveRuns reads every verdict.json under liveRuns.
func readLiveRuns(liveRuns string) ([]recordedRun, error) {
	files, err := filepath.Glob(filepath.Join(liveRuns, "*", "verdict.json"))
	if err != nil {
		return nil, err
	}

	runs := make([]recordedRun, 0, len(files))

	for _, file := range files {
		body, err := os.ReadFile(file) //nolint:gosec // G304: the operator's own live-run store, named by the recipe.
		if err != nil {
			return nil, err
		}

		var verdict LiveRunVerdict
		if err := json.Unmarshal(body, &verdict); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}

		at, err := time.Parse(time.RFC3339, strings.TrimSpace(verdict.Run.StartedAt))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}

		runs = append(runs, recordedRun{file: file, verdict: verdict, at: at})
	}

	slices.SortStableFunc(runs, func(one, other recordedRun) int { return one.at.Compare(other.at) })

	return runs, nil
}

// seedLiveRuns keeps the results of the cases the runs under liveRuns ran
// in latest, by overlay row and case, and gives a comment line for each
// delta run it leaves out.
func seedLiveRuns(liveRuns string, latest map[string]map[string]seededRun) ([]string, error) {
	runs, err := readLiveRuns(liveRuns)
	if err != nil {
		return nil, err
	}

	var leftOut []string

	for _, recorded := range runs {
		verdict := recorded.verdict

		// A run that names its consumer is one case, its scenario with its
		// axis values and release option; a lifecycle's row is qualified
		// with the axis values too.
		row, axes := evidenceKey(verdict.Run), evidenceAxes(verdict.Run)
		if strings.HasSuffix(row, " "+TrialLifecycle) {
			row = strings.TrimSpace(row + " " + axes)
		}

		consumerCase := verdict.Run.Scenario + " " + axes
		if option := consumerSettings(verdict.Run)[settingReleaseOption]; option != "" {
			consumerCase += " " + settingReleaseOption + "=" + option
		}

		rows := preparedCaseEvidence(verdict)
		if row == "" && len(rows) == 0 {
			continue
		}

		if !deltaRunsBefore(runs, recorded) {
			leftOut = append(leftOut, fmt.Sprintf("# left out: %s, the %s set after %s, has no earlier complete run of the %s set on its commit from its prepared directory with its pattern",
				filepath.Base(filepath.Dir(recorded.file)), recorded.set(), recorded.after(), recorded.after()))

			continue
		}

		run := seededRun{at: recorded.at, verdict: verdict.Verdict, commit: verdict.Run.SourceCommit, delta: recorded.after() != ""}

		keepLatest(latest, row, consumerCase, run)

		for row, cases := range rows {
			for key, result := range cases {
				run.verdict = result
				keepLatest(latest, row, key, run)
			}
		}
	}

	return leftOut, nil
}

// preparedCaseEvidence reads a run of the prepared cases (PreparedCases) from
// its tests rather than its header: one process runs many cases, and each
// names itself in its subtests, <test>/<case name>/<visibility>, under the
// test that runs its forge's cases of its event (PreparedCaseTest). It gives
// each case and visibility that ran, <test>/<case name>/<visibility>, by its
// overlay row, and what became of it: failed when a test under it failed,
// passed when all passed, passed on rerun when they passed in the row's
// rerun (attempt 2), and otherwise unfinished. A case with a release option
// is a case of its demo's release row. A lifecycle row is qualified as
// evidenceAxes qualifies one, with the case's own builder and visibility.
func preparedCaseEvidence(verdict LiveRunVerdict) map[string]map[string]string {
	type forgeCase struct {
		PreparedCase

		forge string
	}

	cases := map[string]forgeCase{}

	for _, forge := range []string{forgeForgejo, forgeGitLab} {
		for _, c := range PreparedCases(forge) {
			cases[PreparedCaseTest(forge, c.Event)+"/"+c.Name()] = forgeCase{PreparedCase: c, forge: forge}
		}
	}

	evidence := map[string]map[string]string{}

	for _, test := range verdict.Tests {
		// The test, the four or, with an option, five elements of the case
		// name, the visibility. A visibility is never an option's name.
		elements := strings.Split(test.Name, "/")

		name := 5
		if _, isOption := cases[strings.Join(elements[:min(6, len(elements))], "/")]; isOption {
			name = 6
		}

		if len(elements) <= name {
			continue
		}

		c, isCase := cases[strings.Join(elements[:name], "/")]
		if !isCase {
			continue
		}

		visibility, caseKey := elements[name], strings.Join(elements[:name+1], "/")

		flow := c.Event
		if flow == TrialSnapshot {
			flow = matrixDevelopment
		}

		key := c.forge + " " + c.Demo + " " + flow
		if flow == TrialLifecycle {
			key = strings.Join(slices.Concat(
				[]string{key, settingVisibility + "=" + visibility, settingImageBuilder + "=" + c.Builder}, adminAxes(verdict.Run)), " ")
		}

		result := ""

		switch {
		case test.Result == leafResultPass && test.Attempt > 1:
			result = seedPassedOnRerun
		case test.Result == leafResultPass:
			result = verdictPassed
		case test.Result == leafResultFail:
			result = liveVerdictFailed
		}

		if evidence[key] == nil {
			evidence[key] = map[string]string{}
		}

		// The worst of a case's tests is the case's.
		if had, seen := evidence[key][caseKey]; seen {
			result = worseResult(result, had)
		}

		evidence[key][caseKey] = result
	}

	return evidence
}

// adminAxes are the administrator inputs a run's header records.
func adminAxes(run LiveRunHeader) []string {
	var axes []string

	for _, check := range enforcement.Checks {
		if value := run.Inputs[strings.ToLower(adminInputEnvironment(check))]; value != "" {
			axes = append(axes, adminSetting(check)+"="+value)
		}
	}

	return axes
}

// evidenceKey names the overlay row a run is evidence for, "<provider>
// <demo> <flow>", or nothing for a run of no demo consumer: the demo
// is read from the prepared consumer path <dir>/demos/<demo> (optionally
// ending in /consumer), not the source recipe path. The flow is the recorded
// event or, where the scenario is the flow, the scenario's.
func evidenceKey(run LiveRunHeader) string {
	_, path, found := strings.Cut(filepath.ToSlash(run.Inputs["forge_ci_live_consumer"]), "/demos/")

	demo := strings.TrimSuffix(path, "/consumer")
	if !found || !demoName.MatchString(demo) || !isLabForge(run.Provider) {
		return ""
	}

	flow := run.Inputs["forge_ci_live_consumer_event"]
	if scenario, known := map[string]string{
		"^TestGitLabLayer_ConsumerTrial$": TrialPullRequest, "^TestGitLabRelease_ConsumerTrial$": TrialRelease,
		"^TestGitLabLayer_ConsumerLifecycle$": TrialLifecycle, "^TestForgejoLayer_ConsumerLifecycle$": TrialLifecycle,
		// A snapshot is the development route alone, the matrices' snapshot flow.
		"^TestGitLabLayer_Snapshot$": matrixDevelopment, "^TestForgejoLayer_Snapshot$": matrixDevelopment,
	}[run.Scenario]; known {
		flow = scenario
	}

	if flow == "" {
		return ""
	}

	return run.Provider + " " + demo + " " + flow
}

// evidenceAxes are the axis values a run's verdict header records, as the
// overlay qualifies a mark with them: the visibility (both where none was
// selected), the image builder, the Forgejo case's own CI_IMAGE_BUILDER or
// the lane's selector, and each administrator input. An axis the header does
// not record is left out, so the matrices claim no value for it.
func evidenceAxes(run LiveRunHeader) string {
	input := func(env string) string { return run.Inputs[strings.ToLower(env)] }

	axes := []string{settingVisibility + "=" + cmp.Or(input(consumerVisibilityEnv), visibilityBoth)}

	variables := consumerSettings(run)

	builder, err := "", error(nil)

	switch run.Provider {
	case forgeForgejo:
		builder, err = ForgejoConsumerImageBuilder(variables, input(ForgejoImageBuilderEnv))
	case forgeGitLab:
		builder, _ = imageBuilderValue(input(GitLabImageBuilderEnv))
	}

	if err == nil && builder != "" {
		axes = append(axes, settingImageBuilder+"="+builder)
	}

	return strings.Join(append(axes, adminAxes(run)...), " ")
}

// consumerSettings are the consumer settings a run's header records, by
// name.
func consumerSettings(run LiveRunHeader) map[string]string {
	settings := map[string]string{}

	for line := range strings.SplitSeq(run.Inputs["forge_ci_live_consumer_settings"], "\n") {
		if key, value, isSetting := strings.Cut(line, "="); isSetting {
			settings[key] = value
		}
	}

	return settings
}

// seedPassedOnRerun is a prepared case that passed in its row's rerun, as
// the seed names it; no verdict is spelled so.
const seedPassedOnRerun = "passed on rerun"

func evidenceMark(verdict string) string {
	switch verdict {
	case verdictPassed:
		return evidencePassed
	case seedPassedOnRerun:
		return evidencePassed + evidenceRerunSuffix
	case liveVerdictFailed:
		return "❌"
	default:
		return "⏳"
	}
}
