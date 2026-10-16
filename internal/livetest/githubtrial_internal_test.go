// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-github/v91/github"
)

// The release-request tag is pushed only once GitHub lists every workflow just
// pushed as active: an event before that starts no run.
func TestGitHubListsAllRequiresEveryPushedWorkflowActive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"pullrequest.yml", "release.yml"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	workflow := func(path, state string) *github.Workflow {
		return &github.Workflow{Path: new(".github/workflows/" + path), State: new(state)}
	}

	for name, test := range map[string]struct {
		listed []*github.Workflow
		want   bool
	}{
		"none listed yet": {listed: nil, want: false},
		"one missing":     {listed: []*github.Workflow{workflow("release.yml", "active")}, want: false},
		"one disabled": {listed: []*github.Workflow{
			workflow("release.yml", "active"), workflow("pullrequest.yml", "disabled_manually"),
		}, want: false},
		"all active": {listed: []*github.Workflow{
			workflow("release.yml", "active"), workflow("pullrequest.yml", "active"),
		}, want: true},
	} {
		if got := githubListsAll(&github.Workflows{Workflows: test.listed}, entries); got != test.want {
			t.Errorf("%s: githubListsAll() = %v, want %v", name, got, test.want)
		}
	}
}
