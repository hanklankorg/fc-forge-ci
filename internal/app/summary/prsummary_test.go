// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package summary_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsummary "github.com/diggsweden/forge-ci/internal/app/summary"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func fixedNow() time.Time {
	return time.Date(2026, 5, 10, 14, 30, 0, 0, time.UTC)
}

func TestPRSummary_HappyPath(t *testing.T) {
	t.Parallel()

	sink := &fakeSummarySink{}

	err := appsummary.PRSummary(context.Background(), sink, appsummary.PRSummaryInput{
		ProjectType: "npm",
		Branch:      "feat/foo",
		Commit:      "abcdef0123456789",
		Actor:       "alice",
		RunURL:      "https://example.com/run/1",
		QualityStageResultJSON: stageResultJSON(t, "pr-quality", map[string]string{
			"nanolinter": "success",
			"swift":      "skipped",
		}),
		Now: fixedNow(),
	})
	if err != nil {
		t.Fatal(err)
	}

	body := sink.buf.String()
	for _, want := range []string{
		"# Pull Request Summary",
		"| **Project Type** | `npm` |",
		"| **Branch** | `feat/foo` |",
		"| **Commit** | `abcdef0` |", // truncated
		"| **Checked By** | @alice |",
		"| **Checked At** | 2026-05-10 14:30:00 UTC |",
		"| Nanolinter | ✓ |",
		"| Swift Lint | − |",
		"- [Workflow Run](https://example.com/run/1)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q\nfull:\n%s", want, body)
		}
	}
}

func TestPRSummary_MissingTargetsDefaultToSkipped(t *testing.T) {
	t.Parallel()

	sink := &fakeSummarySink{}

	err := appsummary.PRSummary(context.Background(), sink, appsummary.PRSummaryInput{
		ProjectType: "go",
		Branch:      "main",
		Commit:      "abcdef0",
		Actor:       "bot",
		// Empty JSON → all targets surface as "skipped".
	})
	if err != nil {
		t.Fatal(err)
	}

	// The name is the claim: absent targets must render as skipped, not
	// merely "not failed". Counting the ✗ rows left that unchecked -- a
	// build that silently dropped the rows entirely would have passed.
	body := sink.buf.String()
	for _, want := range []string{"| Lint | − |", "| Swift Lint | − |"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q\nfull:\n%s", want, body)
		}
	}

	for _, unwanted := range []string{"| ✗ |", "| ✓ |"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("unexpected %q on empty input:\n%s", unwanted, body)
		}
	}
}

func TestPRSummary_FailureAndSkippedIcons(t *testing.T) {
	t.Parallel()

	sink := &fakeSummarySink{}

	err := appsummary.PRSummary(context.Background(), sink, appsummary.PRSummaryInput{
		ProjectType: "maven",
		Branch:      "feat/my-branch",
		Commit:      "abc1234567890",
		Actor:       "test-user",
		QualityStageResultJSON: stageResultJSON(t, "pr-quality", map[string]string{
			"nanolinter": "failure",
			"swift":      "skipped",
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	body := sink.buf.String()
	for _, want := range []string{"| Nanolinter | ✗ |", "| Swift Lint | − |"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
}

func TestPRSummary_RejectsMalformedStageResultJSON(t *testing.T) {
	t.Parallel()

	sink := &fakeSummarySink{}

	err := appsummary.PRSummary(context.Background(), sink, appsummary.PRSummaryInput{
		QualityStageResultJSON: `{"stage":"pr-quality","targets":{}}`,
	})
	// A version-less envelope came from a step this build does not understand:
	// ErrMalformedInput, not the ErrUsage of a bad flag.
	if !errors.Is(err, errs.ErrMalformedInput) {
		t.Fatalf("err = %v, want ErrMalformedInput", err)
	}

	if !strings.Contains(err.Error(), "unsupported version") {
		t.Errorf("err = %v, want it to name the version problem", err)
	}

	// Refused before rendering: a half-written summary is worse than none.
	if sink.buf.Len() != 0 {
		t.Errorf("appended a summary despite the rejected input:\n%s", sink.buf.String())
	}
}
