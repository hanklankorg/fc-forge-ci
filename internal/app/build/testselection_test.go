// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

type selectionTool func(context.Context, appbuild.GoRunInput) error

func (tool selectionTool) Run(ctx context.Context, in appbuild.GoRunInput) error {
	return tool(ctx, in)
}

const nanoSelection = `{"runs":[
 {"kind":"package-bin","package":"nanolinter","name":"nanolinter","gate":"application"},
 {"kind":"package-bin","package":"nanolinter-ci","name":"nanolinter-ci","gate":"policy"},
 {"kind":"package-test","package":"nanolinter-ci","name":"basegraph_provenance","gate":"policy"},
 {"kind":"package-test","package":"nanolinter-ci","name":"go_parity","gate":"policy"},
 {"kind":"package-test","package":"nanolinter-ci","name":"imageplan_contract","gate":"policy","ignored":true},
 {"kind":"package-test","package":"nanolinter-ci","name":"release_plan","gate":"policy"},
 {"kind":"package-test","package":"nanolinter","name":"architecture_guards","gate":"policy"},
 {"kind":"package-test","package":"nanolinter","name":"configuration_doc_schema","gate":"policy"},
 {"kind":"package-test","package":"nanolinter","name":"dependency_guards","gate":"policy"},
 {"kind":"package-test","package":"nanolinter","name":"docs_workflow_guards","gate":"policy"},
 {"kind":"package-test","package":"nanolinter","name":"workflow_policy","gate":"policy"}
]}`
const selectionCargoPass = "running 1 test\ntest selected ... ok\ntest result: ok. 1 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.00s\n"

func TestNanoSelectionNeverExpandsToAllTargets(t *testing.T) {
	var calls [][]string

	tool := selectionTool(func(_ context.Context, in appbuild.GoRunInput) error {
		calls = append(calls, in.Args)
		_, _ = io.WriteString(in.Stdout, selectionCargoPass)

		return nil
	})

	err := appbuild.CargoTest(t.Context(), tool, io.Discard, io.Discard, appbuild.CargoTestInput{SelectionJSON: nanoSelection})
	if err != nil {
		t.Fatal(err)
	}

	want := make([][]string, 0, 11)
	want = append(want,
		[]string{"test", "--locked", "--package", "nanolinter", "--bin", "nanolinter"},
		[]string{"test", "--locked", "--package", "nanolinter-ci", "--bin", "nanolinter-ci"},
	)

	for _, target := range []string{"basegraph_provenance", "go_parity", "imageplan_contract", "release_plan"} {
		args := []string{"test", "--locked", "--package", "nanolinter-ci", "--test", target}
		if target == "imageplan_contract" {
			args = append(args, "--", "--ignored")
		}

		want = append(want, args)
	}

	for _, guard := range []string{"architecture_guards", "configuration_doc_schema", "dependency_guards", "docs_workflow_guards", "workflow_policy"} {
		want = append(want, []string{"test", "--locked", "--package", "nanolinter", "--test", guard})
	}

	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("unexpected graph: %v", calls)
	}
}

type selectionExitError int

func (status selectionExitError) Error() string { return "native assertion failure" }
func (status selectionExitError) ExitCode() int { return int(status) }

const selectionCargoFailure = "running 1 test\ntest selected ... FAILED\ntest result: FAILED. 0 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.00s\n"

func writeSelectionFailure(in appbuild.GoRunInput) error {
	_, _ = io.WriteString(in.Stdout, selectionCargoFailure)
	_, _ = io.WriteString(in.Stderr, "error: test failed, to rerun pass `--test selected`\n")

	return selectionExitError(101)
}

func TestCargoPolicyGuardsBlockUnderDefaultReportOnlyApplicationPolicy(t *testing.T) {
	for _, name := range []string{"architecture_guards", "configuration_doc_schema", "dependency_guards", "docs_workflow_guards", "workflow_policy", "arbitrary_repository_policy"} {
		t.Run(name, func(t *testing.T) {
			selection := `{"runs":[{"kind":"package-test","package":"any-product","name":"` + name + `","gate":"policy"}]}`

			var receipt bytes.Buffer

			err := appbuild.CargoTest(t.Context(), selectionTool(func(_ context.Context, in appbuild.GoRunInput) error { return writeSelectionFailure(in) }), &receipt, io.Discard, appbuild.CargoTestInput{SelectionJSON: selection})
			if err == nil || strings.Contains(receipt.String(), "result=findings") || !strings.Contains(receipt.String(), "enforcement=true") {
				t.Fatalf("policy assertion waived: %v; %s", err, &receipt)
			}
		})
	}
}

func TestEarlyApplicationFailureCannotHideUnrunLaterExecutable(t *testing.T) {
	selection := `{"runs":[{"kind":"package-bin","package":"app","name":"first","gate":"application"},{"kind":"package-bin","package":"app","name":"later","gate":"application"}]}`

	for _, laterReports := range []bool{false, true} {
		calls := 0

		var receipt bytes.Buffer

		tool := selectionTool(func(_ context.Context, in appbuild.GoRunInput) error {
			calls++
			if calls == 1 {
				return writeSelectionFailure(in)
			}

			if laterReports {
				_, _ = io.WriteString(in.Stdout, selectionCargoPass)
			}

			return nil
		})

		err := appbuild.CargoTest(t.Context(), tool, &receipt, io.Discard, appbuild.CargoTestInput{SelectionJSON: selection})
		if calls != 2 || (err == nil) != laterReports || !strings.Contains(receipt.String(), "result=findings") {
			t.Fatalf("laterReports=%t calls=%d err=%v receipt=%s", laterReports, calls, err, &receipt)
		}
	}
}

func TestSelectedGoAlwaysKeepsRaceTagsAndFreshJSON(t *testing.T) {
	tool := selectionTool(func(_ context.Context, in appbuild.GoRunInput) error {
		want := []string{"test", "-json", "-count=1", "-race", "-buildvcs=false", "-tags", "safe", "-run", "TestSafe", "./internal/...", "./cmd/..."}
		if !reflect.DeepEqual(in.Args, want) || in.Dir != "project" {
			t.Fatalf("unexpected invocation %+v", in)
		}

		_, _ = io.WriteString(in.Stdout, "{\"Action\":\"start\",\"Package\":\"safe\"}\n{\"Action\":\"run\",\"Package\":\"safe\",\"Test\":\"TestSafe\"}\n{\"Action\":\"pass\",\"Package\":\"safe\",\"Test\":\"TestSafe\"}\n{\"Action\":\"pass\",\"Package\":\"safe\"}\n")

		return nil
	})

	err := appbuild.GoTest(t.Context(), tool, io.Discard, io.Discard, appbuild.GoTestInput{Dir: "project", BuildTags: "safe", SelectionJSON: `{"packages":["./internal/...","./cmd/..."],"run":"TestSafe","gate":"policy"}`})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSelectedGoPolicyCannotBeWaivedByApplicationDefault(t *testing.T) {
	for _, gate := range []string{"policy", "application"} {
		t.Run(gate, func(t *testing.T) {
			calls := 0
			tool := selectionTool(func(_ context.Context, in appbuild.GoRunInput) error {
				calls++
				_, _ = io.WriteString(in.Stdout, "{\"Action\":\"start\",\"Package\":\"gommi\"}\n{\"Action\":\"run\",\"Package\":\"gommi\",\"Test\":\"TestPolicy\"}\n{\"Action\":\"fail\",\"Package\":\"gommi\",\"Test\":\"TestPolicy\"}\n{\"Action\":\"fail\",\"Package\":\"gommi\"}\n")

				return selectionExitError(1)
			})

			var receipt bytes.Buffer

			err := appbuild.GoTest(t.Context(), tool, &receipt, io.Discard, appbuild.GoTestInput{SelectionJSON: `{"packages":["./..."],"gate":"` + gate + `"}`})
			if calls != 1 || (err != nil) != (gate == "policy") {
				t.Fatalf("gate=%s calls=%d err=%v receipt=%s", gate, calls, err, &receipt)
			}

			if gate == "policy" && (!strings.Contains(receipt.String(), "enforcement=true") || strings.Contains(receipt.String(), "result=findings")) {
				t.Fatalf("policy waiver: %s", &receipt)
			}
		})
	}
}

func TestEverySelectedCargoInvocationNeedsItsOwnFreshReport(t *testing.T) {
	calls := 0
	tool := selectionTool(func(_ context.Context, in appbuild.GoRunInput) error {
		calls++
		if calls == 1 {
			_, _ = io.WriteString(in.Stdout, selectionCargoPass)
		}

		return nil
	})

	err := appbuild.CargoTest(t.Context(), tool, io.Discard, io.Discard, appbuild.CargoTestInput{SelectionJSON: nanoSelection})
	if !errors.Is(err, errs.ErrValidation) || calls != 2 {
		t.Fatalf("missing second report admitted: calls=%d err=%v", calls, err)
	}
}

func TestReleaseSelectionCannotOverrideSkipRefusalOrRunInvalidSelection(t *testing.T) {
	tool := selectionTool(func(_ context.Context, _ appbuild.GoRunInput) error {
		t.Fatal("tool called before validation")

		return nil
	})

	for _, skip := range []bool{true, false} {
		goSelection, cargoSelection := `{"packages":[]}`, `{"runs":[]}`
		if skip {
			goSelection, cargoSelection = `{"packages":["./..."]}`, nanoSelection
		}

		goErr := appbuild.GoReleaseBuild(t.Context(), nil, tool, nil, io.Discard, io.Discard, appbuild.GoReleaseBuildInput{SkipTests: skip, TestSelectionJSON: goSelection})

		cargoErr := appbuild.CargoReleaseBuild(t.Context(), nil, tool, io.Discard, io.Discard, appbuild.CargoReleaseBuildInput{SkipTests: skip, TestSelectionJSON: cargoSelection})
		for _, err := range []error{goErr, cargoErr} {
			if !errors.Is(err, errs.ErrUsage) {
				t.Fatalf("not refused: %v", err)
			}

			if skip && !strings.Contains(err.Error(), "skip-tests") {
				t.Fatalf("skip refusal lost: %v", err)
			}
		}
	}
}
