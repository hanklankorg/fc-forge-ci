// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// verdict.json records the consumer inputs LiveRunInputs names, and
// the log's header those its loop names: the two lists must stay one, so the
// verdict never takes an input the log's header would not show.
func TestRunLiveTests_HeaderInputsAreTheVerdictsInputs(t *testing.T) {
	t.Parallel()

	script := string(reporoot.ReadFile(t, "scripts/ci/run-live-tests.sh"))

	_, loop, found := strings.Cut(script, "\n  for input in ")
	loop, _, ended := strings.Cut(loop, "; do")

	if !found || !ended {
		t.Fatal("run-live-tests.sh has no header input loop")
	}

	logged := strings.Fields(strings.ReplaceAll(loop, "\\\n", " "))
	if !slices.Equal(logged, LiveRunInputs) {
		t.Fatalf("the header logs %v, verdict.json records %v", logged, LiveRunInputs)
	}
}
