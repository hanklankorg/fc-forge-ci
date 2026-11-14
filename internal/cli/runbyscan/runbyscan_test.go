// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package runbyscan_test

import (
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/cli/runbyscan"
)

func TestInvokedFindsTheLongestKnownPath(t *testing.T) {
	t.Parallel()

	known := map[string]bool{"build go run": true, "artifact download": true, "release publish": true, "platform checkout": true, "validate container-signature": true}

	body := `
run: forge-ci build go run --enforce-application-tests="$X"
run: |
  run_forge_ci artifact download \
    --name x
  "${FORGE_CI_BIN}" --log-level debug release publish --tag v1
  ./cli-prebuilt/forge-ci platform checkout
  # consumers verify with:
  #   forge-ci validate container-signature ghcr.io/o/i@sha256:x
# forge-ci validate container-signature in a YAML comment
uses:diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@v1
description: "forge-ci runtime image carrying Go"
`

	got := runbyscan.Invoked(body, known)
	want := []string{"build go run", "artifact download", "release publish", "platform checkout"}

	if !slices.Equal(got, want) {
		t.Fatalf("Invoked = %q, want %q", got, want)
	}
}

func TestUnitNamesActionsByDirectory(t *testing.T) {
	t.Parallel()

	for rel, want := range map[string]string{
		".forgejo/actions/build-go/action.yml":  ".forgejo/actions/build-go",
		".github/actions/build-darwin-cli/x.sh": ".github/actions/build-darwin-cli",
		".github/workflows/build-go.yml":        ".github/workflows/build-go.yml",
		"templates/build-go.yml":                "templates/build-go.yml",
	} {
		if got := runbyscan.Unit(rel); got != want {
			t.Errorf("Unit(%q) = %q, want %q", rel, got, want)
		}
	}
}
