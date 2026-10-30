// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import "testing"

func TestManualDispatchRequiresAuthoritativeTrigger(t *testing.T) {
	for _, tc := range []struct {
		event, trigger, want string
		match                bool
	}{
		{"", "workflow_dispatch", "workflow_dispatch", true},
		{"workflow_dispatch", "workflow_dispatch", "workflow_dispatch", true},
		{"", "", "workflow_dispatch", false},
		{"push", "workflow_dispatch", "workflow_dispatch", false},
		{"", "push", "push", false},
		{"", "pull_request", "pull_request", false},
	} {
		if got := runMatchesEvent(WorkflowRun{Event: tc.event, TriggerEvent: tc.trigger}, tc.want); got != tc.match {
			t.Fatalf("event%q trigger%q expected%q: %t", tc.event, tc.trigger, tc.want, got)
		}
	}
}
