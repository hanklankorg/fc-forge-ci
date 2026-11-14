// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabrender

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestConfigProblemsReportsCyclesAndInvalidParentsWithoutRecursingForever(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]map[string]any{
		"cycle": {
			".first": map[string]any{"extends": ".second"}, ".second": map[string]any{"extends": ".first"},
			"build": map[string]any{"extends": ".first", "script": []any{"build"}},
		},
		"wrong parent type":  {"build": map[string]any{"extends": []any{42}, "script": []any{"build"}}},
		"wrong extends type": {"build": map[string]any{"extends": true, "script": []any{"build"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if problems := ConfigProblems(body); len(problems) == 0 {
				t.Fatal("invalid inheritance rendered without a diagnostic")
			}
		})
	}
}

func TestInterpolationPreservesBooleanAndArrayTypesAndRefusesMissingInputs(t *testing.T) {
	t.Parallel()

	values := map[string]any{"inputs.enabled": false, "inputs.commands": []any{"check", "test"}}
	input := map[string]any{"enabled": "$[[ inputs.enabled ]]", "commands": "$[[ inputs.commands ]]"}

	got, err := interpolate(input, values)
	if err != nil || !reflect.DeepEqual(got, map[string]any{"enabled": false, "commands": []any{"check", "test"}}) {
		t.Fatalf("interpolation lost typed inputs: got=%v err=%v", got, err)
	}

	if _, err := interpolate("prefix $[[ inputs.missing ]]", values); !errors.Is(err, errUndeclaredInput) {
		t.Fatalf("missing component input was not refused: %v", err)
	}
}

func TestCallerValidationExpandsTheComponentJobs(t *testing.T) {
	t.Parallel()

	body := "include:\n  - component: gitlab.example/team/engine/check@" + strings.Repeat("a", 40) + "\n    inputs:\n      forge-ci-repository: team/engine\n      server-url: https://gitlab.example\n      job-image: ''\n      commands: [make check]\n"

	problems := DocumentProblems(t, "caller.yml", []byte(body))
	if !strings.Contains(strings.Join(problems, "\n"), "image name is empty") {
		t.Fatalf("caller validation did not inspect the included job: %v", problems)
	}
}

func TestCallerValidationDoesNotFillMissingRequiredInputs(t *testing.T) {
	t.Parallel()

	body := "include:\n  - component: gitlab.example/team/engine/check@" + strings.Repeat("a", 40) + "\n    inputs:\n      forge-ci-repository: team/engine\n      server-url: https://gitlab.example\n      job-image: registry.invalid/tools@sha256:" + strings.Repeat("b", 64) + "\n"

	problems := DocumentProblems(t, "caller.yml", []byte(body))
	if !strings.Contains(strings.Join(problems, "\n"), "required input commands is missing") {
		t.Fatalf("caller received a synthetic check command: %v", problems)
	}
}

func TestComponentValidationRefusesUndeclaredGivenInputs(t *testing.T) {
	t.Parallel()

	problems := Problems(t, "templates/check.yml", map[string]any{"unknown-input": true})
	if !strings.Contains(strings.Join(problems, "\n"), "unknown-input is not declared") {
		t.Fatalf("unknown component input was silently ignored: %v", problems)
	}
}

func TestLiteralRulesUseTheFirstMatchingRule(t *testing.T) {
	t.Parallel()

	falseRule := map[string]any{"if": `"true" == "false"`}
	trueRule := map[string]any{"if": `"true" == "true"`}

	for name, tc := range map[string]struct {
		rules []any
		want  bool
	}{
		"later condition":  {[]any{falseRule, trueRule}, true},
		"fallback":         {[]any{falseRule, map[string]any{"when": "always"}}, true},
		"first match wins": {[]any{map[string]any{"if": `"true" == "true"`, "when": "never"}, trueRule}, false},
		"no match":         {[]any{falseRule}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := RulesAdmit(tc.rules); got != tc.want {
				t.Fatalf("admission=%t, want %t", got, tc.want)
			}
		})
	}
}
