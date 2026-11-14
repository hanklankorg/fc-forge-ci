// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	rerunPackage = "/src/test/live/conformance"
	rerunRow     = "^TestTrial$/^lane$/^(public)$"
	rerunLeaf    = "TestTrial/lane/public"
	// The engine's error on a host it could not reach, and the runner's
	// account of the step's exit status, as a failed job's log has them.
	rerunUnreached = `        2026-11-11T12:23:38.9347345Z Error: download t.tar.gz: dial tcp: lookup github.com on 10.201.0.1:53: i/o timeout: dependency unavailable`
	rerunExit69    = `        2026-11-11T12:23:39.0000000Z exitcode '69': failure`
	rerunMark      = liveConclusionPrefix + rerunLeaf
	rerunEnded     = `    layer_test.go:321: run 7 of release.yml ended "failure", want success`
)

// rowLog is the log of one row's process: its leaf's output, how the leaf
// ended ("" for a test with no result), how the launcher says the process
// ended, and whether the launcher marked it a rerun.
func rowLog(output, leaf, ended string, rerun bool) string {
	lines := []string{livePackageBegin + rerunPackage}
	if rerun {
		lines = append(lines, liveRerunPrefix+rerunRow)
	}

	lines = append(lines, "=== RUN   TestTrial", liveScenarioPrefix+"TestTrial", "=== RUN   TestTrial/lane", "=== RUN   "+rerunLeaf)
	if output != "" {
		lines = append(lines, output)
	}

	if leaf != "" {
		lines = append(lines, "        --- "+leaf+": "+rerunLeaf+" (2.00s)", "    --- "+leaf+": TestTrial/lane (2.00s)", "--- "+leaf+": TestTrial (2.00s)")
	}

	return strings.Join(append(lines, liveRowPrefix+ended+" 12m03s "+rerunRow, livePackageEnd+rerunPackage), "\n") + "\n"
}

// forgejoJob is a failed Forgejo job as the harness prints it: its status,
// then its log under the heading that names it.
func forgejoJob(name string, log ...string) string {
	return strings.Join(append([]string{`    layer_test.go:210: failure   job "` + name + `"`, `    layer_test.go:226: job "` + name + `" log (tail):`}, log...), "\n")
}

// gitlabJobs are the jobs of a GitLab pipeline as the harness prints them:
// each one's status, then their traces as one entry, each under its heading,
// then the end of that entry again under a heading that names no job.
func gitlabJobs(traces ...[]string) string {
	statuses, entry := make([]string, 0, len(traces)), make([]string, 0, 3*len(traces))

	for _, trace := range traces {
		statuses = append(statuses, "    gitlabconsumertrial_test.go:318: job "+trace[0]+": "+trace[1])
		entry = append(entry, "        === job "+trace[0]+" ("+trace[1]+") ===")
		entry = append(entry, trace[2:]...)
	}

	return strings.Join(slices.Concat(statuses, []string{"    gitlabconsumertrial_test.go:395: "}, entry,
		[]string{"    gitlabconsumertrial_test.go:396: the end of the failed job's trace:"}, entry[len(entry)-1:]), "\n")
}

// unreachedRow is the output of a leaf whose one failed Forgejo job an
// unreached host explains, with the harness's mark of the run's conclusion.
func unreachedRow() string {
	return strings.Join([]string{forgejoJob("Build: image", rerunUnreached, rerunExit69), rerunMark, rerunEnded}, "\n")
}

// The rule of the one rerun: a row's first process that failed, the
// conclusion of its forge run the test's first failure, and every failed job
// of that run ended by the engine on a host it could not reach. Each other
// case here is one the rule must refuse.
func TestRerunRowsAreTheRowsThatFailedOnAnUnreachedHostAlone(t *testing.T) {
	t.Parallel()

	const (
		gitlabUnreached = `        2026-11-11T14:41:28.633311Z 01E Error: pull base image: Get "https://gcr.io/v2/": dial tcp: lookup gcr.io on 10.201.0.1:53: read udp 10.201.13.222:52721->10.201.0.1:53: i/o timeout: dependency unavailable`
		buildUnreached  = `        2026-11-11T22:50:47.9359746Z Error: build image: buildah bud: exit status 125: creating build container: initializing source docker://gcr.io/distroless/cc-debian13@sha256:e792ab3d241a468a4fd7519ddbbebe66b49b5f365771716ea688ad40b6c6f1c2: pinging container registry gcr.io: Get "https://gcr.io/v2/": dial tcp: lookup gcr.io on 10.201.0.1:53: server misbehaving: dependency unavailable`
		gitlabExit69    = `        2026-11-11T14:41:29.000000Z 00O ERROR: Job failed: exit code 69`
		gitlabEnded     = "    gitlabrelease_test.go:1500: native release pipeline ended failed"
	)

	if got, want := unreachedCause(buildUnreached), "infrastructure: name resolution (gcr.io)"; got != want {
		t.Errorf("unreachedCause of a build = %q, want %q", got, want)
	}

	failed := func(output string) string { return rowLog(output, testFail, liveVerdictFailed, false) }
	lines := func(lines ...string) string { return strings.Join(lines, "\n") }

	for _, tc := range []struct {
		name, log string
		rerun     bool
	}{
		{"a Forgejo job the engine ended on an unreached host", failed(unreachedRow()), true},
		{"a GitLab job the engine ended on an unreached host", failed(lines(
			gitlabJobs([]string{"Prepare: changelog", "success", "        ok"}, []string{"Build: image", "failed", gitlabUnreached, gitlabExit69}), rerunMark, gitlabEnded)), true},
		{"two failed jobs, both on an unreached host", failed(lines(forgejoJob("Build: image", rerunUnreached, rerunExit69),
			forgejoJob("Check: tests", rerunUnreached, rerunExit69), rerunMark, rerunEnded)), true},
		{"a build whose base image's registry was not reached", failed(lines(forgejoJob("Build: image", buildUnreached, rerunExit69), rerunMark, rerunEnded)), true},
		{"a called job on an unreached host, and its caller the forge fails with it", failed(lines(`    layer_test.go:210: failure   caller "prepare"`,
			forgejoJob("Prepare: changelog", rerunUnreached, rerunExit69), rerunMark, rerunEnded)), true},
		{"a caller the forge failed with no failed job of its own", failed(lines(`    layer_test.go:210: failure   caller "prepare"`, rerunMark, rerunEnded)), false},
		{"a pass", rowLog("", testPass, verdictPassed, false), false},

		// 1: another failure beside it.
		{"an assertion that failed before the conclusion, which is then not marked", failed(lines(forgejoJob("Build: image", rerunUnreached, rerunExit69),
			"    layer_test.go:402: the release has no asset", rerunEnded)), false},
		{"a run the wait gave up because it could never end", failed(lines(`    workflowrun.go:371: livetest: forgejo:   job "Publish: image": blocked`,
			`    workflowrun.go:223: livetest: run 7 of release.yml can never end: no job of it is running or waiting for a runner, and for 10m0s these have stayed blocked on jobs that will not run: "Publish: image"`)), false},
		{"no conclusion of a run at all", failed(lines(forgejoJob("Build: image", rerunUnreached, rerunExit69), "    layer_test.go:402: image digest differs")), false},
		{"another failed job with no engine error", failed(lines(forgejoJob("Build: image", rerunUnreached, rerunExit69),
			forgejoJob("Check: tests", "        [ERROR] BUILD FAILURE", "        exitcode '1': failure"), rerunMark, rerunEnded)), false},
		{"another failed job with another engine error", failed(lines(forgejoJob("Build: image", rerunUnreached, rerunExit69),
			forgejoJob("Check: tests", "        Error: tests failed: validation failed", "        exitcode '1': failure"), rerunMark, rerunEnded)), false},
		{"another failed job whose log the harness did not print", failed(lines(forgejoJob("Build: image", rerunUnreached, rerunExit69),
			`    layer_test.go:210: failure   job "Check: tests"`, `    layer_test.go:228: job "Check: tests": the forge kept no log`, rerunMark, rerunEnded)), false},
		{"a job that failed on something unnamed after the engine's error", failed(lines(
			forgejoJob("Build: image", rerunUnreached, "        mvn: BUILD FAILURE", "        exitcode '1': failure"), rerunMark, rerunEnded)), false},
		{"a GitLab job that exited otherwise after the engine's error", failed(lines(
			gitlabJobs([]string{"Build: image", "failed", gitlabUnreached, "        2026-11-11T14:41:29.000000Z 00O ERROR: Job failed: exit code 1"}), rerunMark, gitlabEnded)), false},
		{"an earlier unreached host and a last engine error of another class", failed(lines(
			forgejoJob("Build: image", rerunUnreached, "        Error: artifact build-dist not found", rerunExit69), rerunMark, rerunEnded)), false},
		{"a job that did not succeed and has no engine error", failed(lines(forgejoJob("Build: image", rerunUnreached, rerunExit69),
			`    layer_test.go:210: cancelled job "Publish: release"`, `    layer_test.go:226: job "Publish: release" log (tail):`, "        the job was cancelled", rerunMark, rerunEnded)), false},
		{"a job that went on after the exit status 69 and exited otherwise", failed(lines(
			forgejoJob("Build: image", rerunUnreached, rerunExit69, "        mvn: BUILD FAILURE", "        exitcode '1': failure"), rerunMark, rerunEnded)), false},
		{"a GitLab job that went on after the exit status 69 and exited otherwise", failed(lines(
			gitlabJobs([]string{"Build: image", "failed", gitlabUnreached, "        section_end: exit status 69", "        2026-11-11T14:41:30.000000Z 00O ERROR: Job failed: exit code 1"}), rerunMark, gitlabEnded)), false},
		{"two failed jobs of one name, one without its log", failed(lines(forgejoJob("Build: image", rerunUnreached, rerunExit69),
			`    layer_test.go:210: failure   job "Build: image"`, `    layer_test.go:228: job "Build: image": the forge kept no log`, rerunMark, rerunEnded)), false},
		{"a failure of the parent's own before its test started", strings.Replace(failed(unreachedRow()), "=== RUN   TestTrial/lane\n",
			"=== RUN   TestTrial/lane\n    trial_test.go:80: the lane's checkout differs\n", 1), false},
		{"a failure of the parent's own after its test started", failed(lines(unreachedRow(), "=== NAME  TestTrial/lane", "    trial_test.go:90: the lane's packages differ")), false},
		{"a parent that failed with no failed test below it", failed(lines(unreachedRow(), "=== RUN   TestTrial/other", "=== RUN   TestTrial/other/leaf",
			"        --- PASS: TestTrial/other/leaf (1.00s)", "    --- FAIL: TestTrial/other (1.00s)")), false},

		// 2: the line where the harness did not attribute it to a failed job.
		{"the line in the output of a command the harness printed", failed(lines(`    layer_test.go:210: failure   job "Build: image"`,
			"    layer_test.go:150: git push:", rerunUnreached, rerunExit69, rerunMark, rerunEnded)), false},
		{"the line under the end of a trace, which names no job", failed(lines("    gitlabconsumertrial_test.go:318: job Build: image: failed",
			"    gitlabconsumertrial_test.go:396: the end of the failed job's trace:", gitlabUnreached, gitlabExit69, rerunMark, gitlabEnded)), false},
		{"the line in the trace of a job that passed", failed(lines(gitlabJobs([]string{"Build: image", "success", gitlabUnreached, gitlabExit69},
			[]string{"Publish: release", "failed", "        ERROR: Job failed: exit code 1"}), rerunMark, gitlabEnded)), false},
		{"a job heading a Forgejo job printed in its own log, before it failed otherwise", failed(lines(forgejoJob("Build: image", rerunUnreached, rerunExit69,
			"        === job Build: image (success) ===", "        Error: tests failed: validation failed", "        exitcode '1': failure"), rerunMark, rerunEnded)), false},
		{"the phrase a program of the job printed", failed(lines(forgejoJob("Build: image", "        2026-11-11T09:56:02Z [INFO] mirror said: dependency unavailable", rerunExit69), rerunMark, rerunEnded)), false},
		{"the line a program of the job echoed before it failed otherwise", failed(lines(forgejoJob("Build: image", rerunUnreached, "        exitcode '1': failure"), rerunMark, rerunEnded)), false},
		{"the phrase in the harness's own line", failed(lines(`    layer_test.go:210: failure   job "Build: image"`,
			`    trial_test.go:70: livetest: Error: Get "https://forge.example/api": dial tcp: lookup forge.example: i/o timeout: dependency unavailable`, rerunMark, rerunEnded)), false},
		{"the mark of another test", failed(lines(forgejoJob("Build: image", rerunUnreached, rerunExit69), liveConclusionPrefix+"TestTrial/lane/private", rerunEnded)), false},

		{"a job's log kept at cleanup, with no job listed as failed", failed(lines("    evidence.go:183: job 4711 log (tail):", gitlabUnreached, gitlabExit69, rerunMark, gitlabEnded)), false},
		{"an exit status 69 before a later error that exited otherwise", failed(lines(
			forgejoJob("Build: image", rerunUnreached, rerunExit69, rerunUnreached, "        exitcode '1': failure"), rerunMark, rerunEnded)), false},

		// 3: an unavailable dependency that answered.
		{"transport evidence in an error of another class", failed(lines(forgejoJob("Build: image",
			"        Error: get release: lookup github.com on 10.201.0.1:53: i/o timeout: rate limited", rerunExit69), rerunMark, rerunEnded)), false},
		{"a server that answered 500", failed(lines(forgejoJob("Publish: release",
			"        Error: upload release asset: 500 Internal Server Error: dependency unavailable", rerunExit69), rerunMark, rerunEnded)), false},
		{"a registry that answered with an empty manifest", failed(lines(forgejoJob("Publish: release",
			"        Error: registry returned an empty manifest for app@sha256:0000: dependency unavailable", rerunExit69), rerunMark, rerunEnded)), false},
		{"a tool that exited with no transport evidence", failed(lines(forgejoJob("Scan: image",
			"        Error: staged linux/amd64: trivy image exited with status 1: dependency unavailable", rerunExit69), rerunMark, rerunEnded)), false},
		{"a limited rate", failed(lines(forgejoJob("Publish: release", "        Error: get release: 429: rate limited", rerunExit69), rerunMark, rerunEnded)), false},

		// The process, not the tests.
		{"the time limit, after a test that failed on it", rowLog(lines(unreachedRow(), "        --- FAIL: "+rerunLeaf+" (2.00s)",
			"=== RUN   TestTrial/lane/private", "panic: test timed out after 3h0m0s"), "", liveVerdictFailed, false), false},
		{"an interrupt", rowLog(unreachedRow(), testFail, verdictInterrupted, false), false},
		{"the lab's mark", failed(lines(liveInfrastructurePrefix+rerunLeaf+" Build: runner_system_failure", unreachedRow())), false},
		{"a rerun that failed on it again", rowLog(unreachedRow(), testFail, liveVerdictFailed, true), false},
		{"a first attempt its rerun replaced", failed(unreachedRow()) + rowLog("", testPass, verdictPassed, true), false},
	} {
		rows, err := RerunRows(strings.NewReader(tc.log))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if tc.rerun != (len(rows) == 1) || tc.rerun && !strings.HasPrefix(rows[0], rerunPackage+"\t"+rerunRow+"\tinfrastructure: name resolution (") {
			t.Errorf("%s: rerun %q, want one: %t", tc.name, rows, tc.rerun)
		}
	}
}

// markTB is a test that has or has not failed when its run's conclusion is
// judged.
type markTB struct {
	recordingTB

	failed bool
}

func (tb *markTB) Failed() bool { return tb.failed }
func (*markTB) Name() string    { return "TestTrial/lane/public" }

// The harness marks a run's conclusion only as a test's first failure, and
// ends the test there either way, so the mark says that no other oracle of
// the test failed.
func TestFailOnRunConclusionMarksOnlyAFirstFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}

		stdout := os.Stdout
		os.Stdout = write

		tb := &markTB{failed: failed}
		FailOnRunConclusionf(tb, "run %d ended %q, want success", 7, "failure")

		os.Stdout = stdout

		if err := write.Close(); err != nil {
			t.Fatal(err)
		}

		printed, err := io.ReadAll(read)
		if err != nil {
			t.Fatal(err)
		}

		want := liveConclusionPrefix + "TestTrial/lane/public\n"
		if failed {
			want = ""
		}

		if string(printed) != want || tb.fatal != `run 7 ended "failure", want success` {
			t.Errorf("failed before %t: printed %q, fatal %q", failed, printed, tb.fatal)
		}
	}
}

// A row that passed on its rerun passes the run, and the verdict cannot be
// read as a pass at the first attempt: it lists the row with both attempts,
// the first with its cause, its tests and its lines of the log, the rerun's
// tests are marked attempt 2, and the Live result line says so.
func TestReadLiveRunRecordsBothAttemptsOfARerunRow(t *testing.T) {
	t.Parallel()

	log := rowLog(unreachedRow(), testFail, liveVerdictFailed, false) + rowLog("", testPass, verdictPassed, true)

	verdict, err := ReadLiveFocusedRun(strings.NewReader(log), 0)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := WriteLiveRunVerdict(dir, verdict); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(body), `"reruns": [`) || !strings.Contains(string(body), `"attempts": [`) || !strings.Contains(string(body), `"attempt": 2`) {
		t.Fatalf("verdict.json:\n%s", body)
	}

	var got LiveRunVerdict
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}

	if got.Verdict != verdictPassed || verdict.ExitCode != 0 || len(got.Tests) != 1 || got.Tests[0].Result != leafResultPass || got.Tests[0].Attempt != 2 {
		t.Fatalf("verdict.json:\n%s", body)
	}

	if len(got.Reruns) != 1 || got.Reruns[0].Row != rerunRow || got.Reruns[0].Package != rerunPackage || got.Reruns[0].Result != verdictPassed || len(got.Reruns[0].Attempts) != 2 {
		t.Fatalf("reruns:\n%s", body)
	}

	const cause = "infrastructure: name resolution (github.com)"

	first, second := got.Reruns[0].Attempts[0], got.Reruns[0].Attempts[1]
	if first.Result != liveVerdictFailed || first.Duration != "12m03s" || first.Cause != cause || first.Log != "live-run.log:1-16" ||
		len(first.Tests) != 1 || first.Tests[0].Result != leafResultFail || first.Tests[0].Cause != cause {
		t.Errorf("first attempt %+v", first)
	}

	if second.Result != verdictPassed || second.Cause != "" || second.Log != "live-run.log:17-27" || len(second.Tests) != 0 {
		t.Errorf("rerun %+v", second)
	}

	if result := verdict.Result(); result.ExitCode != 0 || !strings.HasPrefix(result.Verdict, "passed: 1 row(s) on a rerun after an unavailable dependency") {
		t.Errorf("Live result: %+v", result)
	}
}

// A row that fails its rerun is failed, with both attempts recorded. A rerun
// replaces nothing but a first attempt that was to be run again: a failure
// of another kind stands whatever follows it, and so does a second rerun's
// predecessor.
func TestReadLiveRunKeepsAFailureARerunDoesNotReplace(t *testing.T) {
	t.Parallel()

	unavailable := rowLog(unreachedRow(), testFail, liveVerdictFailed, false)
	defect := rowLog(forgejoJob("Build: image", "        Error: artifact build-dist not found", "        exitcode '1': failure")+"\n"+rerunMark+"\n"+rerunEnded, testFail, liveVerdictFailed, false)
	passed := rowLog("", testPass, verdictPassed, true)

	for _, tc := range []struct {
		name, log    string
		exit, reruns int
		verdict      string
	}{
		{"the rerun fails", unavailable + rowLog(unreachedRow(), testFail, liveVerdictFailed, true), 1, 1, liveVerdictFailed},
		{"a rerun of another failure", defect + passed, 0, 0, liveVerdictFailed},
		{"a second rerun", unavailable + rowLog(unreachedRow(), testFail, liveVerdictFailed, true) + passed, 0, 1, liveVerdictFailed},
		{"no rerun", unavailable, 1, 0, liveVerdictFailed},
	} {
		verdict, err := ReadLiveFocusedRun(strings.NewReader(tc.log), tc.exit)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if verdict.Verdict != tc.verdict || verdict.ExitCode == 0 || len(verdict.Reruns) != tc.reruns {
			t.Errorf("%s: %s, exit %d, reruns %+v", tc.name, verdict.Verdict, verdict.ExitCode, verdict.Reruns)
		}

		if tc.reruns == 1 && verdict.Reruns[0].Result != liveVerdictFailed {
			t.Errorf("%s: the row's result is %q", tc.name, verdict.Reruns[0].Result)
		}
	}
}
