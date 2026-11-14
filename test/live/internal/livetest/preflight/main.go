// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Command preflight validates the live tier's inputs without touching a
// provider: the contract for this suite's lab account and its CA. The run
// script calls it before building anything, so a missing or stale contract is
// reported in seconds with where the suite looked, and an accepted one is
// explained: endpoints, capabilities, credential lifetime and the run identity
// the suite's prefix derives.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

const imageBuildSettingsOptionsMode = "image-build-settings-options"

func main() {
	if len(os.Args) == 4 && os.Args[1] == "classify-run" {
		printRunClass(os.Args[2], os.Args[3])

		return
	}

	if len(os.Args) == 3 && os.Args[1] == "rerun-rows" {
		if err := printRerunRows(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "rerun-rows: %v\n", err)
			os.Exit(2)
		}

		return
	}

	// This options mode also resolves an empty list from inherited settings.
	if len(os.Args) >= 3 || (len(os.Args) == 2 && os.Args[1] == imageBuildSettingsOptionsMode) {
		if err := withArguments(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", os.Args[1], err)
			os.Exit(2)
		}

		return
	}
	// `preflight runner-arch` prints only the architecture the lab's runners
	// execute, which the run script builds the runner binaries for.
	// `preflight consumer-trial-layer` prints only the URL a Forgejo consumer
	// trial serves the layer under test from, which the lab's kaniko job image
	// names as its source. `preflight host-tools` refuses when a pinned host
	// tool the scenarios run does not resolve to a real executable.
	if len(os.Args) == 2 {
		fact, err := preflightFact(os.Args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "live preflight: %v\n", err)
			os.Exit(2)
		}

		fmt.Println(fact)

		return
	}

	if len(os.Args) != 1 {
		usage()
	}

	printLiveInputs()
}

// printRunClass prints the exit code and the verdict of a live run that
// ended with status, as its output file records it.
func printRunClass(statusWord, output string) {
	status, err := strconv.Atoi(statusWord)
	if err != nil || status < 0 || status > 255 {
		usage()
	}

	result, err := classifyRun(status, output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "live result: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("%d %s\n", result.ExitCode, result.Verdict)
}

// printRerunRows prints the rows of a run's retained log that the run script
// runs once more, each as its package, its pattern and its cause
// (livetest.RerunRows).
func printRerunRows(path string) error {
	log, err := os.Open(path) //nolint:gosec // Explicit operator-owned retained log.
	if err != nil {
		return err
	}

	defer func() { _ = log.Close() }()

	rows, err := livetest.RerunRows(log)
	for _, row := range rows {
		fmt.Println(row)
	}

	return err
}

// printLiveInputs checks the live inputs and prints what the run selects.
func printLiveInputs() {
	summary, err := livetest.CheckLiveInputs(time.Now())
	if err == nil {
		// The launcher runs this in the checkout whose orchestrators the run tests.
		err = livetest.CheckAdminInputsConsumed(".", os.Getenv)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "live preflight: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("live preflight: %d provider(s), generation %s, namespace %s\n\n%s",
		summary.Selected, summary.Generation, livetest.ResourcePrefix, summary.Explanation)
}

// classifyRun classifies the run and writes its verdict.json beside the
// retained log. A clean exit must have a readable log to be judged; any other
// status is a failure the log can at most explain, so a log that cannot be
// opened or read then leaves the status to classify it alone.
func classifyRun(status int, path string) (livetest.LiveRunResult, error) {
	var log io.Reader

	file, err := os.Open(path) //nolint:gosec // Explicit operator-owned retained log.
	if err == nil {
		defer func() { _ = file.Close() }()

		log = file
	} else if status == 0 {
		return livetest.LiveRunResult{}, err
	}

	// A log that breaks off fails a clean exit; a failed one keeps its status.
	read := livetest.ReadLiveRun
	if livetest.PreflightRun() {
		read = livetest.ReadLivePreflightRun
	} else if livetest.FocusedRun() {
		read = livetest.ReadLiveFocusedRun
	}

	verdict, err := read(log, status)
	if err != nil && status == 0 {
		return livetest.LiveRunResult{}, err
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "live result: %v\n", err)
	}

	verdict.Run = livetest.LiveRunHeaderFrom(os.Getenv)
	relativePackages(&verdict)

	// The verdict file serves tooling; the Live result line stays the
	// human's, so a verdict that cannot be written does not change it.
	if writeErr := livetest.WriteLiveRunVerdict(filepath.Dir(path), verdict); writeErr != nil {
		fmt.Fprintf(os.Stderr, "live result: %v\n", writeErr)
	}

	// For whoever watches: the failed tests whose job the network stopped.
	for _, test := range verdict.Tests {
		if test.Cause != "" {
			fmt.Fprintf(os.Stderr, "Live cause: %s: %s\n", test.Name, test.Cause)
		}
	}

	return verdict.Result(), nil
}

// relativePackages names each package by its directory relative to the
// repository, where the run script runs the classifier, not by the absolute
// path the log has. A path it cannot relate is kept as it is.
func relativePackages(verdict *livetest.LiveRunVerdict) {
	root, err := os.Getwd()
	if err != nil {
		return
	}

	relative := func(dir string) string {
		if rel, relErr := filepath.Rel(root, dir); relErr == nil && filepath.IsLocal(rel) {
			return "./" + filepath.ToSlash(rel)
		}

		return dir
	}

	for i := range verdict.Tests {
		verdict.Tests[i].Package = relative(verdict.Tests[i].Package)
	}

	for i := range verdict.Infrastructure {
		verdict.Infrastructure[i].Package = relative(verdict.Infrastructure[i].Package)
	}

	for i := range verdict.Reruns {
		verdict.Reruns[i].Package = relative(verdict.Reruns[i].Package)

		for _, attempt := range verdict.Reruns[i].Attempts {
			for j := range attempt.Tests {
				attempt.Tests[j].Package = relative(attempt.Tests[j].Package)
			}
		}
	}
}

// prepared runs a mode of `just test-live-prepare`. `prepared-env-check
// <file>` validates a prepared.env before the prepare puts it in place;
// `prepared-runtime-targets <repository> <forgejo|gitlab|lane>...
// [flavor=<job-image flavor>...]` prints the runtime targets the prepare
// builds for those lanes of the Go tables, one a line, and refuses a case
// whose nanolinter image is of another flavor. The focused modes are of
// `just test-live-prepared` (focused).
func prepared(mode string, args []string) error {
	switch {
	case mode == "prepared-env-check" && len(args) == 1:
		_, err := livetest.ReadPreparedEnvFile(args[0])

		return err
	case mode == "prepared-runtime-targets" && len(args) >= 2:
		var names, flavors []string

		for _, arg := range args[1:] {
			if flavor, isFlavor := strings.CutPrefix(arg, "flavor="); isFlavor {
				flavors = append(flavors, flavor)
			} else {
				names = append(names, arg)
			}
		}

		targets, err := livetest.PreparedRuntimeTargets(args[0], names, flavors)
		for _, target := range targets {
			fmt.Println(target)
		}

		return err
	case strings.HasPrefix(mode, "focused-") && len(args) == 1:
		return focused(mode, args[0])
	default:
		usage()

		return nil
	}
}

// focused runs a mode of `just test-live-prepared`'s pattern run.
// `focused-rows <go test -run pattern>` prints the pattern of each table
// row it selects, which the run script runs a process each;
// `focused-scenarios <go test -run pattern>` prints the pattern of what the
// run set runs beside them, empty for nothing, and `focused-skip <go test
// -run pattern>` what a delta run of the full set leaves out of that.
func focused(mode, run string) error {
	switch mode {
	case "focused-rows":
		rows, err := livetest.FocusedRowRuns(run)
		for _, row := range rows {
			fmt.Println(row)
		}

		return err
	case "focused-scenarios":
		return printPattern(livetest.FocusedScenarioRun(run))
	case "focused-skip":
		return printPattern(livetest.FocusedScenarioSkip(run))
	default:
		usage()

		return nil
	}
}

// printPattern prints a pattern Go gave without an error.
func printPattern(pattern string, err error) error {
	if err == nil {
		fmt.Println(pattern)
	}

	return err
}

func printAffectedRows(forge, set string, changed io.Reader) error {
	paths, err := io.ReadAll(changed)
	if err != nil {
		return err
	}

	rows, everything, err := livetest.AffectedRows(forge, set, strings.FieldsFunc(string(paths), func(r rune) bool { return r == '\n' }))
	if err != nil {
		return err
	}

	for _, path := range everything {
		fmt.Fprintln(os.Stderr, "every row: "+path)
	}

	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no live row of "+forge)
	}

	for _, row := range rows {
		fmt.Println(row)
	}

	return nil
}

// withArguments runs the modes that take arguments: a testing document is
// printed, every other mode is a prepare mode.
func withArguments(mode string, args []string) error {
	if mode == imageBuildSettingsOptionsMode || (mode == "image-build-settings" && len(args) == 4) {
		var (
			settings []string
			err      error
		)
		if mode == imageBuildSettingsOptionsMode {
			settings, err = livetest.ResolveImageBuildOptions(args, os.Getenv)
		} else {
			settings, err = livetest.ResolveImageBuildInvocation(args[0], args[1], args[2], args[3], os.Getenv)
		}

		if err != nil {
			return err
		}

		_, err = fmt.Println(strings.Join(settings, "\n"))

		return err
	}

	if mode == "image-build-options" {
		return livetest.AdmitImageBuildOptions(args, os.Getenv)
	}

	if mode == "image-build-resources" && len(args) == 4 {
		return livetest.AdmitImageBuildInvocation(args[0], args[1], args[2], args[3], os.Getenv)
	}

	// `preflight prepared-source <dir> <head> <git status --porcelain>`
	// refuses a run of prepared images over other source than their commit.
	if mode == "prepared-source" && len(args) == 3 {
		return livetest.CheckPreparedSource(args[0], args[1], args[2])
	}

	if strings.HasPrefix(mode, "testing-") && len(args) <= 2 {
		document, err := testingDocument(mode, args)
		if err != nil {
			return err
		}

		_, err = fmt.Print(document)

		return err
	}

	if ran, err := runSetMode(mode, args); ran {
		return err
	}

	return prepared(mode, args)
}

// runSetMode runs a mode of the run sets, and reports whether mode is one.
// `affected-rows <forgejo|gitlab|github> <set>` is of `just
// test-live-affected`: it reads changed paths, one a line, and prints the
// rows of the set a change of them can affect, and to standard error each
// path whose effect is every row (livetest.AffectedRows). `github-cells
// <set> [setting...]` is of `just test-live-github-cells`: the hosted cells
// of the set, each with its settings and the run's, or the refusal of one
// that could not run. Neither runs anything.
func runSetMode(mode string, args []string) (bool, error) {
	switch {
	case mode == "affected-rows" && len(args) == 2:
		return true, printAffectedRows(args[0], args[1], os.Stdin)
	case mode == "github-cells" && len(args) >= 1:
		cells, err := livetest.GitHubCellRuns(".", args[0], args[1:], os.Getenv)
		for _, cell := range cells {
			fmt.Println(cell)
		}

		return true, err
	default:
		return false, nil
	}
}

// testingDocument renders a testing document. `testing-matrices <repository>`
// prints docs/testing-matrices.md with its generated sections regenerated,
// for `just gen-testing-matrices`; `testing-evidence <live-runs>`
// drafts the live-evidence overlay from the verdicts recorded there.
func testingDocument(mode string, args []string) (string, error) {
	switch {
	case mode == "testing-matrices" && len(args) == 1:
		page, err := os.ReadFile(filepath.Join(args[0], livetest.TestingMatricesPage)) //nolint:gosec // G304: the repository's own page, named by the recipe.
		if err != nil {
			return "", err
		}

		return livetest.RenderTestingMatrices(args[0], string(page))
	case mode == "testing-evidence" && len(args) == 1:
		return livetest.SeedTestingEvidence(args[0])
	default:
		usage()

		return "", nil
	}
}

// preflightFact returns the single fact one preflight mode prints.
func preflightFact(mode string) (string, error) {
	switch mode {
	case "gitlab-consumer-scenario":
		settings, err := livetest.ParseGitLabReleaseSettings(os.Getenv("FORGE_CI_LIVE_CONSUMER_SETTINGS"))
		if err != nil {
			return "", err
		}

		return livetest.GitLabConsumerReleaseScenario(settings.Scenario)
	case "gitlab-lifecycle-settings":
		_, err := livetest.ParseGitLabLifecycleSettings(os.Getenv("FORGE_CI_LIVE_CONSUMER_SETTINGS"))

		return "lifecycle settings accepted", err
	case "signing-inputs":
		err := livetest.CheckReusedSigningInputs(os.Getenv("FORGE_CI_LIVE_SIGNING_KEY_DIR"))

		return "reused signing input metadata accepted (key validity remains signer-verified)", err
	case "runner-arch":
		return livetest.LabRunnerArch()
	case "provider-account":
		return livetest.LabProviderAccount()
	case "consumer-trial-layer":
		return livetest.ConsumerTrialLayerURL()
	case "host-tools":
		return "host tools resolved: " + strings.Join(livetest.LauncherHostTools, " "), livetest.CheckHostTools(context.Background(), livetest.LauncherHostTools)
	default:
		usage()

		return "", nil
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "       preflight image-build-resources <default|required> <cpus-or-empty> <memory-MiB-or-empty> <compiler-jobs-or-empty> (validate requested limits; enforcement unmeasured)")
	fmt.Fprintln(os.Stderr, "       preflight classify-run <exit-code> <retained-log>")
	fmt.Fprintln(os.Stderr, "       preflight focused-rows <go-test-run-pattern> (the pattern of each table row it selects; reads FORGE_CI_LIVE_PROVIDER, FORGE_CI_LIVE_SET and FORGE_CI_LIVE_AFTER)")
	fmt.Fprintln(os.Stderr, "       preflight focused-scenarios <go-test-run-pattern> (the pattern of what the run set runs beside the rows; empty for nothing)")
	fmt.Fprintln(os.Stderr, "       preflight focused-skip <go-test-run-pattern> (the pattern of the scenarios a delta run of the full set leaves out; empty for none)")
	fmt.Fprintln(os.Stderr, "       preflight affected-rows <forgejo|gitlab|github> <small|medium|full> (changed paths on standard input; the rows of the run set they can affect)")
	fmt.Fprintln(os.Stderr, "       preflight github-cells <small|medium|full> [setting...] (reads FORGE_CI_LIVE_AFTER; the hosted GitHub cells of the run set, as <visibility><tab><demo><tab><event><tab><settings>)")
	fmt.Fprintln(os.Stderr, "       preflight rerun-rows <retained-log> (the rows that failed on an unreached host alone, as <package><tab><pattern><tab><cause>)")
	fmt.Fprintln(os.Stderr, "       preflight prepared-env-check <prepared.env>")
	fmt.Fprintln(os.Stderr, "       preflight gitlab-consumer-scenario | gitlab-lifecycle-settings (validates FORGE_CI_LIVE_CONSUMER_SETTINGS)")
	fmt.Fprintln(os.Stderr, "       preflight prepared-runtime-targets <repository> <forgejo|gitlab|lane>... [flavor=<flavor>...]")
	fmt.Fprintln(os.Stderr, "       preflight testing-matrices <repository> | testing-evidence <live-runs>")
	fmt.Fprintln(os.Stderr, "usage: preflight [signing-inputs | runner-arch | provider-account | consumer-trial-layer | host-tools] (signing-inputs checks only FORGE_CI_LIVE_SIGNING_KEY_DIR; host-tools checks only this host; other modes read the forgeci lab contract)")
	os.Exit(2)
}
