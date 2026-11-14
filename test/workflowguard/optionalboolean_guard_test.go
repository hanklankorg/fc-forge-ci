// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// comparedWithFalse matches an expression that compares a value with false or
// null to tell "unset" from "off".
var comparedWithFalse = regexp.MustCompile(`[!=]= *(false|null)\b`)

// GitHub's expressions compare loosely: an unset value and false are both 0,
// so `value != false` is false for an unset value and a setting that defaults
// to true was turned off. include-aab read that way and a release of an
// Android application built no AAB; enable-code-signing read that way too.
// A default-true setting compares its text: format('{0}', value) != 'false'.
func TestWorkflowsDoNotCompareAnOptionalValueWithFalse(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(reporoot.Path(t), ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".yml" {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)

		for number, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") || !strings.Contains(line, "${{") {
				continue
			}

			if comparedWithFalse.MatchString(line) {
				t.Errorf("%s:%d compares with false or null, which an unset value equals: %s", entry.Name(), number+1, strings.TrimSpace(line))
			}
		}
	}
}
