// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package fakeoutputsink_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

func TestSink_MultilineOwnershipAndAbsence(t *testing.T) {
	t.Parallel()

	sink := fakeoutputsink.New(t)
	require.Nil(t, sink.Multiline("missing"))

	for _, lines := range [][]string{nil, {}} {
		require.NoError(t, sink.SetMultiline(t.Context(), "empty", lines))
		got := sink.Multiline("empty")
		require.NotNil(t, got)
		require.Empty(t, got)
	}

	lines := []string{"first", "second"}
	require.NoError(t, sink.SetMultiline(t.Context(), "tags", lines))
	lines[0] = "caller mutation"
	snapshot := sink.Multiline("tags")
	require.Equal(t, []string{"first", "second"}, snapshot)
	snapshot[1] = "snapshot mutation"

	require.Equal(t, []string{"first", "second"}, sink.Multiline("tags"))
	require.Equal(t, []string{"empty", "tags"}, sink.Keys())
	require.Nil(t, sink.Multiline("missing"))
}

func TestSink_RejectsAfterClose(t *testing.T) {
	t.Parallel()

	s := fakeoutputsink.New(t)
	ctx := context.Background()

	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if err := s.Set(ctx, "k", "v"); err == nil {
		t.Errorf("Set after Close did not return error")
	}

	if err := s.SetMultiline(ctx, "k", []string{"v"}); err == nil {
		t.Errorf("SetMultiline after Close did not return error")
	}

	if got := s.CloseCount(); got != 1 {
		t.Errorf("CloseCount = %d, want 1", got)
	}
}

func TestSink_AllScalar_IsCopy(t *testing.T) {
	t.Parallel()

	s := fakeoutputsink.New(t)
	_ = s.Set(context.Background(), "k", "v")

	all := s.AllScalar()
	all["k"] = "tampered"

	if got := s.Single("k"); got != "v" {
		t.Errorf("AllScalar mutation leaked into sink: got %q", got)
	}
}

// TestSink_RefusesANewlineWithoutChangingState covers the guard that keeps a
// multi-line value from being written as a scalar.
//
// The real GHA sink writes `key=value` on one line, so a scalar containing a
// newline would inject an additional, attacker- or accident-controlled output
// line. The fake refuses instead, and what matters as much as the refusal is
// that it changes nothing: a test asserting "the refused key was not published"
// needs the sink to still be empty afterwards.
func TestSink_RefusesANewlineWithoutChangingState(t *testing.T) {
	t.Parallel()

	sink := fakeoutputsink.New(t)

	if err := sink.Set(context.Background(), "kept", "fine"); err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{"a\nb", "a\rb", "trailing\n"} {
		if err := sink.Set(context.Background(), "injected", value); err == nil {
			t.Errorf("Set accepted a scalar containing a newline: %q", value)
		}
	}

	if got := sink.Keys(); len(got) != 1 || got[0] != "kept" {
		t.Errorf("keys = %v, want only [kept]: a refused write left state behind", got)
	}

	if got := sink.Single("injected"); got != "" {
		t.Errorf("the refused key holds %q", got)
	}
}
