// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// Shared with the live owner so ordinary offline tests check the actual graph.
//
//go:embed testdata/layer/consumer-pr.yml
var layerConsumerPRFixture string

// The consumer's Go source, small enough to read here. The live owner commits
// it; the lint cases' offline tests vet the same bytes.
const (
	consumerPRMain = `package main

import "fmt"

var version = "development"

func banner() string {
	return "consumer-pr-canary " + version
}

func main() {
	fmt.Println(banner())
}
`

	consumerPRMainTest = `package main

import "testing"

func TestBanner(t *testing.T) {
	if got := banner(); got != "consumer-pr-canary "+version {
		t.Fatalf("banner() = %q", got)
	}
}
`
)

const consumerPRAssertionFailure = `package main

import "testing"

func TestControlledApplicationFinding(t *testing.T) {
	t.Fatal("controlled application assertion")
}
`

// Valid Go syntax, but no test can run: this is an operational compiler error,
// not an assertion finding eligible for report-only continuation.
const consumerPRCompileError = `package main

import "testing"

func TestControlledApplicationFinding(t *testing.T) {
	enforcementCompileError(t)
}
`

var consumerPRAdverseTests = []struct{ name, source string }{
	{"application-test-findings", consumerPRAssertionFailure},
	{"application-test-compile-error", consumerPRCompileError},
}

func TestConsumerPRFixtureGatesTheDownstreamJob(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Needs []string `yaml:"needs"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(layerConsumerPRFixture), &workflow); err != nil {
		t.Fatal(err)
	}

	if needs := workflow.Jobs["consumer-test"].Needs; len(needs) != 1 || needs[0] != "consumer-build" {
		t.Fatalf("downstream identity job must depend on the actual build action, got %v", needs)
	}
}

// Execute the same tiny test sources that the maintained PR owner commits.
// This is an offline classifier witness, not native action/job execution.
func TestConsumerPRAdverseSourcesAreFindingsAndOperationalError(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range consumerPRAdverseTests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"go.mod":       "module example.invalid/enforcement-fixture\n\ngo 1.27.4\n",
				"main_test.go": test.source,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, goBinary, "test", "-json", "-count=1", "./...")
			cmd.Dir = dir

			var stdout, stderr bytes.Buffer

			cmd.Stdout, cmd.Stderr = &stdout, &stderr

			var exit *exec.ExitError

			if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("fixture must fail through Go, not a shell exit: %v\n%s\n%s", err, &stdout, &stderr)
			}

			output := enforcement.NewTestOutput(enforcement.GoJSON)
			_, _ = output.Stdout().Write(stdout.Bytes())
			_, _ = output.Stderr().Write(stderr.Bytes())

			want, marker := 1, "controlled application assertion"
			if test.source == consumerPRCompileError {
				want, marker = 0, "undefined: enforcementCompileError"
			}

			if got := output.FailedTests(); got != want || !strings.Contains(stdout.String()+stderr.String(), marker) {
				t.Fatalf("classified %d assertion failures, want %d and %q\n%s\n%s", got, want, marker, &stdout, &stderr)
			}
		})
	}
}

// The lint cases' workflow, shared with the live owner like the build's.
//
//go:embed testdata/layer/consumer-pr-lint.yml
var layerConsumerPRLintFixture string

const (
	// The lint cases' consumer plans one check, the Go toolchain's own, so the
	// gate's verdict needs no scanner database and no network.
	consumerPRLintPlan = "[verify]\nchecks = [\"go-vet\"]\n"

	// The Go the shipped Go consumer declares, which its lint image carries.
	consumerPRLintGoMod = "module example.invalid/consumer-pr-canary\n\ngo 1.26\n"

	// It compiles and its tests pass; go vet alone reports the assignment.
	consumerPRLintFinding = `package main

func controlledLintFinding() int {
	value := 1
	value = value

	return value
}
`

	consumerPRLintFindingMarker = "self-assignment of value"

	// The build fixture's job image: it holds Go and no nanolinter, so the
	// lint gate cannot run in it.
	consumerPRLinterlessImage = "docker.io/library/golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9"
)

// consumerPRLintBase is what the lint cases' consumer holds on main beside
// its workflows: the module, at the Go its lint image carries, and the plan.
var consumerPRLintBase = map[string]string{
	"go.mod": consumerPRLintGoMod, "main.go": consumerPRMain, "main_test.go": consumerPRMainTest,
	"nanolinter.toml": consumerPRLintPlan,
}

// The action's own refusal as the job prints it, to the end of its line: the
// same words inside an echoed script are followed by its quote.
var consumerPRLinterMissing = regexp.MustCompile(`(?m)ERROR: nanolinter is not on PATH: run this job in a nanolinter image, or on a runner that provides it\r?$`)

// consumerPRLintLogProblems holds a lint job's log to what its case must
// leave there. A finding is go vet's own, classified by the engine as one
// lint finding under the administrator's setting and as no vulnerability. A
// gate that cannot run says so itself and reaches no policy decision.
func consumerPRLintLogProblems(log string, unavailable, enforced bool) []string {
	var problems []string

	if unavailable {
		if !consumerPRLinterMissing.MatchString(log) {
			problems = append(problems, "lint log lacks the gate's refusal to run without nanolinter")
		}

		if strings.Contains(log, "Enforcement policy default-sha256:") {
			problems = append(problems, "a gate that could not run reached a policy decision")
		}

		return problems
	}

	for _, want := range []string{
		consumerPRLintFindingMarker,
		fmt.Sprintf("lint result=findings count=1 enforcement=%t source=flag", enforced),
		"vulnerabilities result=clean count=0",
	} {
		if !strings.Contains(log, want) {
			problems = append(problems, fmt.Sprintf("lint log lacks %q", want))
		}
	}

	return problems
}

// The lint fixture's graph and call are what the cases rely on: the
// downstream job waits for the gate alone, the gate is the layer's action
// with one required offline check, and the administrator's settings, which
// the action declares, are left to the renderer.
func TestConsumerPRLintFixtureCallsTheGateWithoutAdminSettings(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Needs     []string `yaml:"needs"`
			Container struct {
				Image string `yaml:"image"`
			} `yaml:"container"`
			Steps []struct {
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(layerConsumerPRLintFixture), &workflow); err != nil {
		t.Fatal(err)
	}

	if needs := workflow.Jobs["consumer-test"].Needs; len(workflow.Jobs) != 2 || len(needs) != 1 || needs[0] != "consumer-lint" {
		t.Fatalf("downstream identity job must depend on the lint gate alone, got %v of %d jobs", needs, len(workflow.Jobs))
	}

	lint := workflow.Jobs["consumer-lint"]
	if lint.Container.Image != "RENDER_LINT_JOB_IMAGE" || len(lint.Steps) != 2 {
		t.Fatalf("the test must name the lint job's image and the job must only check out and lint: %+v", lint)
	}

	gate := lint.Steps[1]
	if gate.Uses != "https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/lint-nanolinter@PIN_FORGE_CI_SHA" ||
		!maps.Equal(gate.With, map[string]string{"required-lints": "go-vet", "offline": "true"}) {
		t.Fatalf("the gate must be the layer's action with one required offline check and no enforce-* setting: %+v", gate)
	}

	var action struct {
		Inputs map[string]any `yaml:"inputs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/actions/lint-nanolinter/action.yml"), &action); err != nil {
		t.Fatal(err)
	}

	for _, setting := range []string{"enforce-lint", "enforce-vulnerabilities"} {
		if _, declared := action.Inputs[setting]; !declared {
			t.Errorf("the lint action no longer declares %s, so the renderer would not bind it", setting)
		}
	}

	if !strings.Contains(layerConsumerPRFixture, "image: "+consumerPRLinterlessImage+"\n") {
		t.Error("the control's image must be the build fixture's own Go image, which holds no nanolinter")
	}
}

// The lint consumer's sources through the Go toolchain's own vet: clean
// without the adverse file and one finding with it, while the module still
// compiles. This is an offline witness of the source, not of the gate.
func TestConsumerPRLintSourceIsAVetFindingAndTheBaseIsClean(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}

	for _, adverse := range []bool{false, true} {
		dir := writeConsumerPRLintConsumer(t, adverse)

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		for _, verb := range []string{"build", "vet"} {
			cmd := exec.CommandContext(ctx, goBinary, verb, "./...")
			cmd.Dir = dir

			output, err := cmd.CombinedOutput()

			found := strings.Contains(string(output), "lint.go:5:2: "+consumerPRLintFindingMarker)
			if wantFinding := adverse && verb == "vet"; (err != nil) != wantFinding || found != wantFinding {
				t.Fatalf("go %s with the adverse source %t: %v, finding reported %t\n%s", verb, adverse, err, found, output)
			}
		}
	}
}

// The same sources through nanolinter and the engine's own gate, as the
// action runs them: the finding is one lint finding and no vulnerability,
// reported under report-only policy and blocking under enforcement, and the
// log oracle tells the three outcomes apart. It needs a nanolinter on PATH
// and is no native evidence: this nanolinter is the host's, not a job image's.
func TestConsumerPRLintGateClassifiesTheFinding(t *testing.T) {
	linter, err := exec.LookPath("nanolinter")
	if err != nil {
		t.Skip("no nanolinter on PATH, so the gate's classification of the lint source is not witnessed here")
	}

	gate := func(t *testing.T, adverse, enforce bool) (string, error) {
		t.Helper()

		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()

		ctx = appenforcement.WithAdmin(ctx, enforcement.Admin{
			enforcement.Lint: strconv.FormatBool(enforce), enforcement.Vulnerabilities: "false",
		})

		// One buffer per stream: nanolinter's two are copied concurrently. A
		// job log interleaves them; the oracle reads no order.
		var out, stderr bytes.Buffer

		err := appenforcement.NanolinterVerify(ctx, consumerPRLintRunner{linter: linter, dir: writeConsumerPRLintConsumer(t, adverse)},
			&out, &stderr, appenforcement.NanolinterVerifyInput{RequiredLints: "go-vet", Offline: true})

		return out.String() + stderr.String(), err
	}

	reported, err := gate(t, true, false)
	if problems := consumerPRLintLogProblems(reported, false, false); err != nil || len(problems) != 0 {
		t.Fatalf("report-only finding: %v, %v\n%s", err, problems, reported)
	}

	blocked, err := gate(t, true, true)
	if problems := consumerPRLintLogProblems(blocked, false, true); !errors.Is(err, errs.ErrValidation) || len(problems) != 0 {
		t.Fatalf("enforced finding: %v, %v\n%s", err, problems, blocked)
	}

	clean, err := gate(t, false, true)
	if err != nil {
		t.Fatalf("clean source under enforcement: %v\n%s", err, clean)
	}

	for name, unexpected := range map[string][]string{
		"a clean run read as the finding":         consumerPRLintLogProblems(clean, false, true),
		"a report-only run read as enforced":      consumerPRLintLogProblems(reported, false, true),
		"an enforced run read as report-only":     consumerPRLintLogProblems(blocked, false, false),
		"a judged finding read as an absent tool": consumerPRLintLogProblems(reported, true, false),
	} {
		if len(unexpected) == 0 {
			t.Errorf("the log oracle accepted %s", name)
		}
	}
}

// The log oracle on the lines each outcome leaves, as the engine and the
// action write them.
func TestConsumerPRLintLogProblems(t *testing.T) {
	const (
		receipt = "Enforcement policy default-sha256:0000: lint result=findings count=1 enforcement=false source=flag blocking-count=1\n" +
			"Enforcement policy default-sha256:0000: vulnerabilities result=clean count=0 enforcement=false source=flag blocking-count=0\n"
		finding = "lint.go:5:2: " + consumerPRLintFindingMarker + "\n"
		missing = "2026-01-01T00:00:00Z ERROR: nanolinter is not on PATH: run this job in a nanolinter image, or on a runner that provides it\n"
	)

	for _, test := range []struct {
		name, log             string
		unavailable, enforced bool
		problems              int
	}{
		{name: "reported finding", log: finding + receipt},
		{name: "enforced finding", log: finding + strings.ReplaceAll(receipt, "enforcement=false source=flag blocking-count=1", "enforcement=true source=flag blocking-count=1"), enforced: true},
		{name: "another failure of the same check", log: receipt, problems: 1},
		{name: "finding without a decision", log: finding, problems: 2},
		{name: "two findings", log: finding + strings.ReplaceAll(receipt, "count=1", "count=2"), problems: 1},
		{name: "finding counted as a vulnerability", log: finding + strings.ReplaceAll(receipt, "vulnerabilities result=clean count=0", "vulnerabilities result=findings count=1"), problems: 1},
		{name: "the other policy", log: finding + receipt, enforced: true, problems: 1},
		{name: "default policy rather than the administrator's", log: finding + strings.ReplaceAll(receipt, "source=flag", "source=embedded-default"), problems: 1},
		{name: "gate cannot run", log: missing, unavailable: true},
		{name: "gate cannot run, CRLF", log: strings.ReplaceAll(missing, "\n", "\r\n"), unavailable: true},
		{name: "only the echoed script names the refusal", log: `echo "ERROR: nanolinter is not on PATH: run this job in a nanolinter image, or on a runner that provides it" >&2` + "\n", unavailable: true, problems: 1},
		{name: "gate ran and judged", log: missing + finding + receipt, unavailable: true, problems: 1},
		{name: "gate ran instead", log: finding + receipt, unavailable: true, problems: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			if problems := consumerPRLintLogProblems(test.log, test.unavailable, test.enforced); len(problems) != test.problems {
				t.Fatalf("%d problems, want %d: %q", len(problems), test.problems, problems)
			}
		})
	}
}

// writeConsumerPRLintConsumer writes the files the lint cases commit, with
// the pull request's adverse source or without.
func writeConsumerPRLintConsumer(t *testing.T, adverse bool) string {
	t.Helper()

	files := maps.Clone(consumerPRLintBase)
	if adverse {
		files["lint.go"] = consumerPRLintFinding
	}

	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

// consumerPRLintRunner runs the gate's nanolinter in the consumer's
// directory, with the locale the action gives it.
type consumerPRLintRunner struct{ linter, dir string }

func (runner consumerPRLintRunner) Run(ctx context.Context, out, stderr io.Writer, args []string) (int, error) {
	cmd := exec.CommandContext(ctx, runner.linter, args[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = runner.dir, out, stderr

	cmd.Env = append(os.Environ(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "NO_COLOR=1")

	var exit *exec.ExitError
	if err := cmd.Run(); errors.As(err, &exit) {
		return exit.ExitCode(), nil
	} else if err != nil {
		return 0, err
	}

	return 0, nil
}
