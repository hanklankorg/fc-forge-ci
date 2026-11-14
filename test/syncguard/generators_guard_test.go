// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package syncguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// TestEveryGeneratorHasARecipeNamedInASyncTest holds each cmd/gen-* generator
// to two things: a `just gen-*` recipe that runs it, and a sync test whose
// refresh advice names that recipe. A generator without both produces a
// committed file that can drift silently, and looks exactly like the covered
// ones in review.
func TestEveryGeneratorHasARecipeNamedInASyncTest(t *testing.T) {
	t.Parallel()

	justfile := string(reporoot.ReadFile(t, "justfile"))

	var syncTests strings.Builder

	for _, entry := range reporoot.ReadDir(t, "test/syncguard") {
		if strings.HasSuffix(entry.Name(), "_test.go") && entry.Name() != "generators_guard_test.go" {
			syncTests.Write(reporoot.ReadFile(t, "test/syncguard/"+entry.Name()))
		}
	}

	generators := 0

	for _, entry := range reporoot.ReadDir(t, "cmd") {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "gen-") {
			continue
		}

		generators++
		recipe := entry.Name()

		if !strings.Contains(justfile, "\n"+recipe+":\n    @go run ./cmd/"+recipe+" ") {
			t.Errorf("cmd/%s has no `just %s` recipe running it; a generator nobody runs is a file that drifts", recipe, recipe)
		}

		if !strings.Contains(syncTests.String(), `"just `+recipe+`"`) {
			t.Errorf("no sync test tells a contributor to run `just %s`; cmd/%s produces a file nothing compares", recipe, recipe)
		}
	}

	if generators < 3 {
		t.Fatalf("found %d generators under cmd/; the walk, not the tree, is what was measured", generators)
	}
}

// TestGeneratedOutputsAreCheckedByTheOfflineSuite closes the other half of the
// question: the guards must run where CI actually runs them.
//
// `just test` is what the pull-request workflow invokes, and these guards are
// ordinary Go tests under test/syncguard, so they run there by
// construction. What is worth asserting is the part that is NOT structural —
// that the recipes which run one named guard (the convenience `check-*`
// wrappers) still name tests that exist.
func TestGeneratedOutputsAreCheckedByTheOfflineSuite(t *testing.T) {
	t.Parallel()

	justfile := string(reporoot.ReadFile(t, "justfile"))

	// The recipes may point at tests across the guard packages, not only
	// this one, so the search covers all of them rather than a hand-listed few.
	defined := guardTestNames(t, reporoot.Path(t))

	named := regexp.MustCompile(`-run '\^(Test[A-Za-z0-9_]+)\$'`).FindAllStringSubmatch(justfile, -1)
	if len(named) == 0 {
		t.Fatal("no check-* wrapper names a test; the regexp, not the justfile, is what was measured")
	}

	for _, match := range named {
		if !defined[match[1]] {
			t.Errorf("a just recipe runs %q, which no guard package defines; the wrapper passes by selecting nothing",
				match[1])
		}
	}

	if !strings.Contains(string(reporoot.ReadFile(t, filepath.Join(".github", "workflows", "self-pullrequest.yml"))), "just test") {
		t.Error("the pull-request workflow no longer runs `just test`, which is how these guards reach CI at all")
	}
}

// guardTestNames collects every top-level test function defined in the
// repo-wide guard packages, which is the set a check-* wrapper may name.
func guardTestNames(t *testing.T, root string) map[string]bool {
	t.Helper()

	defined := map[string]bool{}
	definition := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)

	for _, pkg := range []string{"archguard", "lexiconguard", "syncguard", "workflowguard"} {
		entries, err := os.ReadDir(filepath.Join(root, "test", pkg))
		if err != nil {
			t.Fatal(err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}

			body := string(reporoot.ReadFile(t, filepath.Join("test", pkg, entry.Name())))
			for _, match := range definition.FindAllStringSubmatch(body, -1) {
				defined[match[1]] = true
			}
		}
	}

	if len(defined) == 0 {
		t.Fatal("no guard tests found; the walk, not the packages, is what was measured")
	}

	return defined
}
