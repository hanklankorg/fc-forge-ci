// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestSnapshotPublicationModes(t *testing.T) {
	files := map[string]filePayload{
		"tool":       {Body: "#!/bin/sh\nexit 0\n", Mode: 0755},
		"library.sh": {Body: "value=fixture\n", Mode: 0644},
		"data":       {Body: "fixture\x00bytes\n", Mode: 0644},
	}
	dir := t.TempDir()
	// Existing permissions must not override the payload's executable intent.
	if err := os.WriteFile(filepath.Join(dir, "tool"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshotWriteFiles(t, dir, files)
	command := exec.CommandContext(t.Context(), "git", "init", "--quiet", dir)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	command = exec.CommandContext(t.Context(), "git", "-C", dir, "-c", "core.filemode=true", "add", "--all")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "ls-files", "--stage").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct{ mode, path string }{{"100755", "tool"}, {"100644", "library.sh"}, {"100644", "data"}} {
		found := false
		for line := range strings.SplitSeq(string(out), "\n") {
			if strings.HasPrefix(line, expected.mode+" ") && strings.HasSuffix(line, "\t"+expected.path) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing Git mode %s for %s: %s", expected.mode, expected.path, out)
		}
	}
}

func TestSnapshotCandidateSourceFidelity(t *testing.T) {
	root := reporoot.Path(t)
	files := snapshotCandidateFiles(t)
	if len(files) == 0 {
		t.Fatal("empty source candidate")
	}
	dir := t.TempDir()
	snapshotWriteFiles(t, dir, files)
	for path, payload := range files {
		name := filepath.Join(root, path)
		before, err := os.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(name)
		if err != nil || string(body) != payload.Body {
			t.Fatalf("candidate differs from fresh working source: %s: %v", path, err)
		}
		after, err := os.Lstat(name)
		if err != nil || before.Mode() != after.Mode() {
			t.Fatalf("original source mode changed: %s: %v", path, err)
		}
		if (before.Mode()&0111 != 0) != (payload.Mode == 0755) {
			t.Fatalf("source executable intent lost: %s", path)
		}
	}
}
