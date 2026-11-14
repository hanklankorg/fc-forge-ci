// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestValidateDevelopmentPush(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*WorkflowRun)
		runs   []WorkflowRun
		want   string
	}{
		{name: "successful push"},
		{name: "listing identity only", runs: []WorkflowRun{{ID: 42}}},
		{name: "failed", change: func(r *WorkflowRun) { r.Status = "failure" }, want: `development push status = "failure", want success`},
		{name: "skipped", change: func(r *WorkflowRun) { r.Status = "skipped" }, want: `development push status = "skipped", want success`},
		{name: "empty status", change: func(r *WorkflowRun) { r.Status = "" }, want: `development push status = "", want success`},
		{name: "dispatch event", change: func(r *WorkflowRun) { r.Event = "workflow_dispatch" }, want: `development push event = "workflow_dispatch", want push`},
		{name: "empty event", change: func(r *WorkflowRun) { r.Event = "" }, want: `development push event = "", want push`},
		{name: "dispatch trigger", change: func(r *WorkflowRun) { r.TriggerEvent = "workflow_dispatch" }, want: `development push trigger event = "workflow_dispatch", want push`},
		{name: "empty trigger", change: func(r *WorkflowRun) { r.TriggerEvent = "" }, want: `development push trigger event = "", want push`},
		{name: "wrong workflow", change: func(r *WorkflowRun) { r.WorkflowID = "release.yml" }, want: `development push workflow = "release.yml", want "development.yml"`},
		{name: "empty workflow", change: func(r *WorkflowRun) { r.WorkflowID = "" }, want: `development push workflow = "", want "development.yml"`},
		{name: "no runs", runs: []WorkflowRun{}, want: "development push run count = 0, want one"},
		{name: "two runs", runs: []WorkflowRun{{ID: 42}, {ID: 43}}, want: "development push run count = 2, want one"},
		{name: "duplicate run", runs: []WorkflowRun{{ID: 42}, {ID: 42}}, want: "development push run count = 2, want one"},
		{name: "foreign run", runs: []WorkflowRun{{ID: 43}}, want: "development push listed run ID = 43, want 42"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := WorkflowRun{ID: 42, Status: "success", Event: "push", TriggerEvent: "push", WorkflowID: "development.yml"}
			if test.change != nil {
				test.change(&run)
			}

			runs := test.runs
			if runs == nil {
				runs = []WorkflowRun{run}
			}

			err := ValidateDevelopmentPush(run, runs, "development.yml")
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}

				return
			}

			if !errors.Is(err, errs.ErrValidation) || err.Error() != test.want+": validation failed" {
				t.Fatalf("got %v, want %s: validation failed", err, test.want)
			}
		})
	}
}

func TestSelectDevelopmentArtifact(t *testing.T) {
	const name = "fc-dev-exact-source"

	for _, test := range []struct {
		name      string
		change    func(*RunArtifact)
		artifacts []RunArtifact
		maxBytes  int64
		want      string
	}{
		{name: "small exact bound", maxBytes: 32 << 10},
		{name: "larger package bound", maxBytes: 32 << 20},
		{name: "package exact bound", maxBytes: 32 << 20, change: func(a *RunArtifact) { a.SizeInBytes = 32 << 20 }},
		{name: "one byte", maxBytes: 32 << 10, change: func(a *RunArtifact) { a.SizeInBytes = 1 }},
		{name: "no artifacts", artifacts: []RunArtifact{}, maxBytes: 32 << 10, want: "development artifact count = 0, want one"},
		{name: "two artifacts", artifacts: []RunArtifact{{Name: name}, {Name: name}}, maxBytes: 32 << 10, want: "development artifact count = 2, want one"},
		{name: "stale source", maxBytes: 32 << 10, change: func(a *RunArtifact) { a.Name = "fc-dev-stale-source" }, want: `development artifact name = "fc-dev-stale-source", want "fc-dev-exact-source"`},
		{name: "empty name", maxBytes: 32 << 10, change: func(a *RunArtifact) { a.Name = "" }, want: `development artifact name = "", want "fc-dev-exact-source"`},
		{name: "expired", maxBytes: 32 << 10, change: func(a *RunArtifact) { a.Expired = true }, want: `development artifact "fc-dev-exact-source" is expired`},
		{name: "zero size", maxBytes: 32 << 10, change: func(a *RunArtifact) { a.SizeInBytes = 0 }, want: `development artifact "fc-dev-exact-source" size = 0, want 1..32768 bytes`},
		{name: "negative size", maxBytes: 32 << 10, change: func(a *RunArtifact) { a.SizeInBytes = -1 }, want: `development artifact "fc-dev-exact-source" size = -1, want 1..32768 bytes`},
		{name: "over small bound", maxBytes: 32 << 10, change: func(a *RunArtifact) { a.SizeInBytes++ }, want: `development artifact "fc-dev-exact-source" size = 32769, want 1..32768 bytes`},
		{name: "over package bound", maxBytes: 32 << 20, change: func(a *RunArtifact) { a.SizeInBytes = 32<<20 + 1 }, want: `development artifact "fc-dev-exact-source" size = 33554433, want 1..33554432 bytes`},
		{name: "zero bound", maxBytes: 0, want: `development artifact "fc-dev-exact-source" size = 32768, want 1..0 bytes`},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := RunArtifact{ID: 17, Name: name, SizeInBytes: 32 << 10}
			if test.change != nil {
				test.change(&artifact)
			}

			artifacts := test.artifacts
			if artifacts == nil {
				artifacts = []RunArtifact{artifact}
			}

			got, err := SelectDevelopmentArtifact(artifacts, name, test.maxBytes)
			if test.want == "" {
				if err != nil || got != artifact {
					t.Fatalf("got %+v, %v; want unchanged %+v", got, err, artifact)
				}

				return
			}

			if !errors.Is(err, errs.ErrValidation) || err.Error() != test.want+": validation failed" || got != (RunArtifact{}) {
				t.Fatalf("got %+v, %v; want zero artifact and %s: validation failed", got, err, test.want)
			}
		})
	}
}
