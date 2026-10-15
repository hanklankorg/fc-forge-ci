// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
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

		start := strings.Index(body, "        (\n          set -euo pipefail\n          expected='$[[ component.sha ]]'")
		end := strings.Index(body[max(start, 0):], "\n        )\n")

		if start < 0 || end < 0 {
			t.Fatalf("templates/%s.yml: engine fetch block not found", name)
		}

		blocks[name] = body[start : start+end]
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
