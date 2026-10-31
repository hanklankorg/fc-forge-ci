// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The two lint components fetch and verify the engine for their SAST
// conversion with the same block. A fix made in one copy and not the other
// would leave one component fetching differently, so the copies must match,
// and both must keep the checksum-bound path the check component uses.
func TestLintComponentsFetchTheEngineTheSameVerifiedWay(t *testing.T) {
	t.Parallel()

	blocks := map[string]string{}

	for _, name := range []string{"nanolinter", "megalinter"} {
		body := string(reporoot.ReadFile(t, "templates/"+name+".yml"))

		start := strings.Index(body, "expected='$[[ component.sha ]]'")
		const verified = "SIGNER_STRICT=1 verify_forge_ci"
		end := strings.Index(body[max(start, 0):], verified)

		if start < 0 || end < 0 {
			t.Fatalf("templates/%s.yml: engine fetch block not found", name)
		}

		lines := strings.Split(body[start:start+end+len(verified)], "\n")
		for index, line := range lines {
			lines[index] = strings.TrimSpace(line)
		}
		blocks[name] = strings.Join(lines, "\n")
		if name == "nanolinter" && start+end > strings.Index(body, "run_forge_ci policy nanolinter") {
			t.Fatal("lint ran before mandatory verified policy bootstrap")
		}
	}

	if blocks["nanolinter"] != blocks["megalinter"] {
		t.Error("templates/nanolinter.yml and templates/megalinter.yml fetch the engine differently")
	}

	for _, want := range []string{
		`fetch --quiet --depth=1 --no-tags --no-recurse-submodules origin "${expected}"`,
		`test "$(git -C "${helpers}" rev-parse HEAD)" = "${expected}"`,
		"SIGNER_STRICT=1 verify_forge_ci",
	} {
		if !strings.Contains(blocks["nanolinter"], want) {
			t.Errorf("the lint components' engine fetch lost %q", want)
		}
	}
}

// A component that runs an image the consumer chooses runs its own script in
// it, so it clears the image's entrypoint: the Docker executor would otherwise
// run the job's shell through it, and a lint image's entrypoint is the linter.
func TestComponentsClearTheEntrypointOfConsumerChosenImages(t *testing.T) {
	t.Parallel()

	for _, use := range []struct{ file, input string }{
		{"check", "job-image"},
		{"nanolinter", "nanolinter-image"},
		{"megalinter", "megalinter-image"},
		{"release-jobs", "job-image"},
		{"release-jobs", "build-image"},
	} {
		body := string(reporoot.ReadFile(t, "templates/"+use.file+".yml"))
		want := "name: $[[ inputs." + use.input + " ]]\n    entrypoint: [\"\"]"

		if strings.Contains(body, "image: $[[ inputs."+use.input+" ]]") || !strings.Contains(body, want) {
			t.Errorf("templates/%s.yml must run %s as image: {name: ..., entrypoint: [\"\"]}", use.file, use.input)
		}
	}
}

// GitLab gives job variables to the helper container that fetches the source
// as well as to the job's image, so a HOME that only the image has breaks the
// fetch ("could not lock config file"). Components set HOME in their script.
func TestComponentsDoNotSetHomeAsAJobVariable(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(filepath.Join(reporoot.Path(t), "templates"))
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}

		for number, line := range strings.Split(string(reporoot.ReadFile(t, "templates/"+entry.Name())), "\n") {
			if strings.HasPrefix(strings.TrimLeft(line, " "), "HOME:") {
				t.Errorf("templates/%s:%d sets HOME as a job variable; export it in the script", entry.Name(), number+1)
			}
		}
	}
}

// Both lint components fetch the merge request's target the same way; a fix
// in one copy alone would leave the other comparing with nothing.
func TestLintComponentsFetchTheMergeRequestTargetTheSameWay(t *testing.T) {
	t.Parallel()

	blocks := map[string]string{}

	for _, name := range []string{"nanolinter", "megalinter"} {
		body := string(reporoot.ReadFile(t, "templates/"+name+".yml"))

		start := strings.Index(body, "      # --- Merge-request base")
		end := strings.Index(body[max(start, 0):], "\n      fi\n\n")

		if start < 0 || end < 0 || !strings.Contains(body, "GIT_DEPTH: \"0\"") {
			t.Fatalf("templates/%s.yml: merge-request base block or GIT_DEPTH 0 not found", name)
		}

		blocks[name] = body[start : start+end]
	}

	if blocks["nanolinter"] != blocks["megalinter"] {
		t.Error("templates/nanolinter.yml and templates/megalinter.yml fetch the merge request's target differently")
	}
}
