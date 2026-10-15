// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

// PAR-UX-3: --json emits the same shape on every forge.
//
// Machine output is a contract with somebody else's script, and the whole point
// of a multi-forge tool is that the script does not have to care which forge it
// ran against. A field that appears only on GitHub, or a capability key that is
// spelled one way on one adapter, forces every consumer into per-forge handling
// — which is the thing this product exists to remove.
//
// Values are expected to differ: that is the forge showing through, and the test
// requires it, because a shape comparison passes trivially if the command
// secretly reported the same forge twice.
//
// `doctor --json` is the surface under test because it is the one that renders
// the most forge-derived state — the detected platform, the runner dialect, and
// the whole capability model — through one schema.

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

func TestJSON_DoctorReport_HasTheSameShapeOnEveryForge(t *testing.T) {
	var (
		mu       sync.Mutex
		shapes   = map[provider.ForgeAPI][]string{}
		forgeAPI = map[provider.ForgeAPI]string{}
	)

	forges := forgesClaiming(t, alwaysValidatesTokens, "a JSON report")

	// The forges run in parallel inside this group, so the comparison below
	// starts only once every forge has reported.
	t.Run("forges", func(t *testing.T) {
		for _, forge := range forges {
			t.Run(string(forge), func(t *testing.T) {
				t.Parallel()

				target := livetest.Accept(t, forge)
				repo := livetest.NewScratchRepo(t, target, "json-shape")

				run := livetest.CLI(t, target, repo, "doctor", "--json")

				// doctor reports problems by exit code; a non-zero exit still has to
				// produce a parsable report, and that is part of the contract.
				var report map[string]any
				if err := json.Unmarshal([]byte(run.Stdout), &report); err != nil {
					t.Fatalf("%s: doctor --json did not emit parsable JSON on stdout (exit %d): %v\nstdout: %s\nstderr: %s",
						forge, run.ExitCode, err, run.Stdout, run.Stderr)
				}

				mu.Lock()
				shapes[forge] = jsonShape(report, "")

				if env, ok := report["environment"].(map[string]any); ok {
					forgeAPI[forge], _ = env["forge_api"].(string)
				}
				mu.Unlock()
			})
		}
	})

	if len(shapes) != len(forges) {
		t.Fatalf("only %d of %d selected forges produced a report", len(shapes), len(forges))
	}

	// Non-vacuity: the reports must actually come from different forges. Without
	// this, a doctor that hard-coded one platform would pass the shape check
	// perfectly.
	for _, forge := range forges {
		if got := forgeAPI[forge]; got != string(forge) {
			t.Errorf("%s reported forge_api %q, so the reports are not from the forges they claim",
				forge, got)
		}
	}

	// The shape must be a real schema, not a nearly-empty document that two
	// forges agree on by accident. These are the deepest paths the report has, so
	// requiring them proves the flattening descended and the comparison below is
	// over something worth comparing.
	// Paths are spelled as jsonShape records them: path, then the JSON kind.
	for _, required := range []string{
		"environment.forge_api:string",
		"environment.runner:string",
		"environment.capabilities.container_tag_deletion:bool",
		"checks[].name:string",
		"checks[].severity:string",
	} {
		for _, forge := range forges {
			if !slices.Contains(shapes[forge], required) {
				t.Errorf("%s: doctor --json has no %s; the shape comparison would prove little",
					forge, required)
			}
		}
	}

	t.Run("cross-forge-parity", func(t *testing.T) {
		if len(forges) < 2 {
			livetest.NotCoveredf(t, "the contract selects %d forge(s); cross-forge parity needs two", len(forges))
		}

		reference := forges[0]
		for _, forge := range forges[1:] {
			if diff := shapeDiff(shapes[reference], shapes[forge]); diff != "" {
				t.Errorf("doctor --json has a different shape on %s than on %s, so a consumer must special-case per forge:\n%s",
					forge, reference, diff)
			}
		}
	})
}
