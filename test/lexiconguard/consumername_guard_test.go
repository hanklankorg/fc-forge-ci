// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package lexiconguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

// downstreamConsumerNames are the projects that consume the engine and that
// its tests once named: as a stand-in payload, image or package name, or in a
// comment saying whose workflow a fixture was. The engine is project-neutral,
// so a test says hello-cli or hello-lib where it needs a name. The names are
// lowercase and specific: it is not a general vocabulary.
//
// The lint engine the product ships under its own name is deliberately absent.
// lint-nanolinter, nanolinter-image and the nanolinter component are the
// product's interface, and the tests of that interface must spell it; so this
// guard cannot see that name used as a stand-in. The generic-cli adapter under
// test/live/fixtures builds that project's real source and names it for that.
func downstreamConsumerNames() []string {
	return []string{"gommitlint", "wallet", "cose-lib", "se.digg.cose"}
}

// consumerNameRoots are the trees held neutral, relative to the repository.
func consumerNameRoots() []string {
	return []string{"test", "scripts/ci"}
}

// consumerNameExceptions are the files that keep a name, each with the reason
// it cannot be reworded. A file listed here is still required to hold one, so
// an exception that has gone stale fails instead of lingering.
func consumerNameExceptions() map[string]string {
	return map[string]string{
		"test/lexiconguard/consumername_guard_test.go": "this guard declares the names",
	}
}

// TestTestTreeNamesNoDownstreamConsumer fails when a file under the neutral
// roots names a downstream consumer, in its path or in its content.
func TestTestTreeNamesNoDownstreamConsumer(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	exceptions := consumerNameExceptions()
	used := make(map[string]bool, len(exceptions))

	var (
		offenders []string
		visited   int
	)

	for _, tree := range consumerNameRoots() {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if !entry.Type().IsRegular() {
				return nil
			}

			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			rel = filepath.ToSlash(rel)

			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			visited++

			name := namedConsumer(rel + "\n" + string(content))
			if name == "" {
				return nil
			}

			if _, excepted := exceptions[rel]; excepted {
				used[rel] = true

				return nil
			}

			offenders = append(offenders, rel+" names "+name)

			return nil
		})
		require.NoError(t, err)
	}

	require.NotZero(t, visited, "the consumer-name guard inspected no files")
	require.Empty(t, offenders,
		"the engine's tests are project-neutral: use a neutral stand-in such as hello-cli, "+
			"or add the file to consumerNameExceptions with the reason it must keep the name")

	for rel, reason := range exceptions {
		require.Truef(t, used[rel], "%s is excepted (%s) but names no downstream consumer; remove the exception", rel, reason)
	}
}

// namedConsumer is the first downstream consumer text names, in any case, or
// "" when it names none.
func namedConsumer(text string) string {
	folded := strings.ToLower(text)

	for _, name := range downstreamConsumerNames() {
		if strings.Contains(folded, name) {
			return name
		}
	}

	return ""
}
