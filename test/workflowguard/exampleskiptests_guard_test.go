// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// skipTestsTrue matches a skip-tests input or SKIP_TESTS variable set to
// true, quoted or not, outside a comment.
var skipTestsTrue = regexp.MustCompile(`(?i)^\s*-?\s*(skip-tests|SKIP_TESTS)\s*:\s*["']?true["']?\s*(#.*)?$`)

// The engine refuses skip-tests=true on every forge: an administrator may
// require tests, and a report-only policy still runs them. The GitLab go-cli
// example passed it, so a merge request copied from it failed at build-go
// before building anything.
func TestExamplesNeverSkipTests(t *testing.T) {
	t.Parallel()

	checked := 0

	err := filepath.WalkDir(filepath.Join(reporoot.Path(t), "examples"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}

		if ext := filepath.Ext(path); ext != ".yml" && ext != ".yaml" {
			return nil
		}

		body, err := os.ReadFile(path)
		require.NoError(t, err)

		checked++

		for number, line := range strings.Split(string(body), "\n") {
			if skipTestsTrue.MatchString(line) {
				t.Errorf("%s:%d sets %q, which the engine refuses", path, number+1, strings.TrimSpace(line))
			}
		}

		return nil
	})
	require.NoError(t, err)
	require.Positive(t, checked, "no example YAML found; the guard checks nothing")
}
