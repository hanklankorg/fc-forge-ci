// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The promote job keeps its promotion journal as a run artifact: the upload
// step runs whatever the steps before it ended as, after the promotion and
// before the step that removes the state directory the journal is in. Its
// own shell runs here against the fake engine: a journal is uploaded alone,
// under the attempt's name, for seven days, and is gone from the runner only
// after that; a job that planned no tag move uploads nothing and succeeds.
func TestPromoteJobUploadsItsJournalOnFailureBeforeCleanup(t *testing.T) {
	t.Parallel()

	const (
		promote = "Promote signed images"
		upload  = "Upload promotion journal"
		cleanup = "Cleanup signer workspace"
	)

	var document struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	loadYAML(t, ".forgejo/workflows/sign-and-publish-release.yml", &document)

	steps := document.Jobs["promote"].Steps
	at := func(name string) int {
		return slices.IndexFunc(steps, func(candidate step) bool { return candidate.Name == name })
	}

	promoted, uploaded, cleaned := at(promote), at(upload), at(cleanup)
	if promoted < 0 || uploaded < promoted || cleaned < uploaded {
		t.Fatalf("promote job runs %q, %q and %q as steps %d, %d and %d, want them in that order",
			promote, upload, cleanup, promoted, uploaded, cleaned)
	}

	if got := steps[uploaded].If; got != "always()" {
		t.Fatalf("%q runs if %q, want always(): a failed promotion is the one whose journal is needed", upload, got)
	}

	if got := steps[uploaded].Env["RUN_ATTEMPT"]; got != "${{ forgejo.run_attempt || 1 }}" {
		t.Fatalf("%q names its artifact by %q, want the run attempt: a re-run must not replace the first journal", upload, got)
	}

	box := newSandbox(t, "sign-and-publish-release")
	state := filepath.Join(box.root, "state")
	environment := []string{"HELPERS_DIR=" + box.root, "SIGN_AND_PUBLISH_STATE_DIR=" + state, "RUN_ATTEMPT=1"}

	if got := box.run(steps[uploaded].Run, environment...); got.status != 0 || box.argv() != nil {
		t.Fatalf("without a journal: status %d and engine arguments %q, want success and no upload\nstderr %s",
			got.status, box.argv(), got.stderr)
	}

	journal := box.write("state/image-promotions.jsonl", `{"version":1}`+"\n", 0o600)
	// Beside the journal, as anything else the job leaves in its state
	// directory would be: it must not be uploaded.
	box.write("state/other", "private", 0o600)

	want := []string{"artifact", "upload", "--name", "release-promote-1", "--file", journal, "--retention-days", "7"}
	if got := box.run(steps[uploaded].Run, environment...); got.status != 0 || !slices.Equal(box.argv(), want) {
		t.Fatalf("with a journal: status %d and engine arguments %q, want %q\nstderr %s",
			got.status, box.argv(), want, got.stderr)
	}

	if got := box.run(steps[cleaned].Run, environment...); got.status != 0 {
		t.Fatalf("cleanup: status %d\nstderr %s", got.status, got.stderr)
	}

	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("cleanup left the journal on the runner (%v), so the order above proves nothing", err)
	}
}
