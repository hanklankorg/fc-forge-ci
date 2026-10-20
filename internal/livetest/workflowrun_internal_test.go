// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// zipEntry is one entry of an archive built for a test.
type zipEntry struct {
	name    string
	content string
	mode    fs.FileMode
}

func buildZIP(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()

	var archive bytes.Buffer

	writer := zip.NewWriter(&archive)

	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)

		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := file.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return archive.Bytes()
}

func TestZIPTree_ReadsEveryFileByName(t *testing.T) {
	t.Parallel()

	archive := buildZIP(t,
		zipEntry{name: ".hidden", content: "hidden\n", mode: 0o600},
		zipEntry{name: "empty", mode: 0o600},
		zipEntry{name: "dir/nested/file", content: "nested\n", mode: 0o755},
	)

	tree, err := ZIPTree(archive, 14)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]byte{".hidden": []byte("hidden\n"), "empty": {}, "dir/nested/file": []byte("nested\n")}
	if !maps.EqualFunc(tree, want, bytes.Equal) {
		t.Errorf("tree = %q, want %q", tree, want)
	}
}

func TestZIPTree_RefusesWhatATreeComparisonCouldNotJudge(t *testing.T) {
	t.Parallel()

	regular := func(name string) zipEntry { return zipEntry{name: name, content: "x", mode: 0o600} }

	for name, archive := range map[string][]byte{
		"not an archive":   []byte("PK is not enough"),
		"absolute name":    buildZIP(t, regular("/etc/passwd")),
		"climbing name":    buildZIP(t, regular("../outside")),
		"unclean name":     buildZIP(t, regular("dir/../file")),
		"backslash name":   buildZIP(t, regular(`dir\file`)),
		"directory entry":  buildZIP(t, zipEntry{name: "dir/", mode: fs.ModeDir | 0o700}),
		"symbolic link":    buildZIP(t, zipEntry{name: "link", content: "target", mode: fs.ModeSymlink | 0o777}),
		"duplicate name":   buildZIP(t, regular("file"), regular("file")),
		"over the limit":   buildZIP(t, zipEntry{name: "file", content: "0123456789abcdefg", mode: 0o600}),
		"over it in total": buildZIP(t, zipEntry{name: "a", content: "0123456789", mode: 0o600}, zipEntry{name: "b", content: "0123456", mode: 0o600}),
	} {
		tree, err := ZIPTree(archive, 16)
		if err == nil || tree != nil {
			t.Errorf("%s: ZIPTree = %q, %v; want a refusal and no tree", name, tree, err)
		}

		if name != "not an archive" && !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%s: refusal %v is not a validation error", name, err)
		}
	}
}

func TestRunIsOver_OnlyForAStatusForgejoWillNotChange(t *testing.T) {
	t.Parallel()

	for status, want := range map[string]bool{
		"success": true, "failure": true, "cancelled": true, "skipped": true, "error": true,
		"": false, "unknown": false, "waiting": false, "running": false, "blocked": false,
	} {
		if got := runIsOver(status); got != want {
			t.Errorf("runIsOver(%q) = %t, want %t", status, got, want)
		}
	}
}

func TestSelectRun_JudgesOnlyTheRunTheAwaitedEventStarted(t *testing.T) {
	t.Parallel()

	run := func(id int64, file, event, trigger string) WorkflowRun {
		return WorkflowRun{ID: id, WorkflowID: file, Event: event, TriggerEvent: trigger}
	}

	const (
		file        = "pullrequest.yml"
		pullRequest = "pull_request"
	)

	for name, test := range map[string]struct {
		runs    []WorkflowRun
		event   string
		wantID  int64
		afterID int64
		refuse  bool
	}{
		"none yet":                         {event: pullRequest},
		"the awaited run":                  {runs: []WorkflowRun{run(3, file, pullRequest, pullRequest)}, event: pullRequest, wantID: 3},
		"another workflow's run is unseen": {runs: []WorkflowRun{run(4, "release.yml", "schedule", "schedule")}, event: pullRequest},
		"a push run beside it is passed over": {
			runs:  []WorkflowRun{run(5, file, eventPush, eventPush), run(6, file, pullRequest, pullRequest)},
			event: pullRequest, wantID: 6,
		},
		"a push run alone is not the awaited one": {runs: []WorkflowRun{run(7, file, eventPush, eventPush)}, event: pullRequest},
		"a push run is the awaited one":           {runs: []WorkflowRun{run(8, file, eventPush, eventPush)}, event: eventPush, wantID: 8},
		"two awaited runs":                        {runs: []WorkflowRun{run(9, file, pullRequest, pullRequest), run(10, file, pullRequest, pullRequest)}, event: pullRequest, refuse: true},
		"two push runs when push is awaited":      {runs: []WorkflowRun{run(11, file, eventPush, eventPush), run(12, file, eventPush, eventPush)}, event: eventPush, refuse: true},
		"an unknown event":                        {runs: []WorkflowRun{run(13, file, "schedule", "schedule")}, event: pullRequest, refuse: true},
		"an empty event":                          {runs: []WorkflowRun{run(14, file, "", "")}, event: pullRequest, refuse: true},
		"a push event another event triggered":    {runs: []WorkflowRun{run(15, file, eventPush, "schedule")}, event: pullRequest, refuse: true},
		"the awaited event, another trigger":      {runs: []WorkflowRun{run(16, file, pullRequest, eventPush)}, event: pullRequest, refuse: true},
		"second release excludes the first":       {runs: []WorkflowRun{run(20, file, eventPush, eventPush), run(21, file, eventPush, eventPush)}, event: eventPush, afterID: 20, wantID: 21},
		"old release is not a new result":         {runs: []WorkflowRun{run(20, file, eventPush, eventPush)}, event: eventPush, afterID: 20},
		"two newer releases remain ambiguous":     {runs: []WorkflowRun{run(21, file, eventPush, eventPush), run(22, file, eventPush, eventPush)}, event: eventPush, afterID: 20, refuse: true},
	} {
		got, passedOver, err := selectRun(test.runs, file, test.event, test.afterID)

		switch {
		case test.refuse:
			if !errors.Is(err, errs.ErrValidation) || got.ID != 0 {
				t.Errorf("%s: selectRun = %+v, %v; want a refusal", name, got, err)
			}
		case err != nil || got.ID != test.wantID:
			t.Errorf("%s: selectRun = run %d, %v; want run %d", name, got.ID, err, test.wantID)
		}

		// A push run is passed over, and named, only when another event is awaited.
		wantPassed := 0

		if !test.refuse && test.event != eventPush {
			for _, run := range test.runs {
				if run.WorkflowID == file && run.Event == eventPush && run.ID > test.afterID {
					wantPassed++
				}
			}
		}

		if len(passedOver) != wantPassed {
			t.Errorf("%s: %d runs passed over, want %d", name, len(passedOver), wantPassed)
		}
	}
}
