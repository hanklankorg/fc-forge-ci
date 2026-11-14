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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

func TestJSON_DoctorReport_HasTheSameShapeOnEveryForge(t *testing.T) {
	livetest.RegisterLiveScenario(t)

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

// A repository without .forge-ci/artifacts.yml is still configured when its
// root holds one ecosystem manifest, and is refused when it holds two: a
// polyglot root has no single artifact to derive.
//
// GitHub's reusable workflows reach that rule through the planner. Forgejo and
// GitLab reach it through the CLI alone, and doctor is the verb that reports
// which way it went, so the two answers are read from its checks here, on
// every forge, in a directory holding nothing but the manifests.
func TestDoctor_AutoDerive_ReadsOneRootManifestAndRefusesAPolyglotRoot(t *testing.T) {
	livetest.RegisterLiveScenario(t)

	const (
		check   = "configuration auto-derives"
		goMod   = "module example.invalid/autoderive-canary\n\ngo 1.27.4\n"
		cargo   = "[package]\nname = \"autoderive-canary\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"
		derived = `no .forge-ci/artifacts.yml; using the go artifact "autoderive-canary" derived from the root manifest (write the file to configure more)`
	)

	for _, forge := range forgesClaiming(t, alwaysValidatesTokens, "a JSON report") {
		t.Run(string(forge), func(t *testing.T) {
			t.Parallel()

			target := livetest.Accept(t, forge)
			repo := livetest.NewScratchRepo(t, target, "doctor-autoderive")

			for _, tc := range []struct {
				name      string
				manifests map[string]string
				severity  string
				message   []string
			}{
				{"one-manifest", map[string]string{"go.mod": goMod}, "ok", []string{derived}},
				// Both manifests are named, in the order the probes list them.
				{"polyglot-root", map[string]string{"go.mod": goMod, "Cargo.toml": cargo}, "fail", []string{
					"multiple manifests at root: Cargo.toml, go.mod",
					"Cannot auto-derive — a polyglot repo needs an explicit artifacts.yml.",
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					root := t.TempDir()
					for file, content := range tc.manifests {
						if err := os.WriteFile(filepath.Join(root, file), []byte(content), 0o600); err != nil {
							t.Fatal(err)
						}
					}

					run := livetest.CLIIn(t, target, repo, livetest.RunOptions{Dir: root}, "doctor", "--json")

					var report struct {
						Checks []struct {
							Name     string `json:"name"`
							Severity string `json:"severity"`
							Message  string `json:"message"`
						} `json:"checks"`
						Failures int `json:"failures"`
					}
					if err := json.Unmarshal([]byte(run.Stdout), &report); err != nil {
						t.Fatalf("doctor --json did not emit parsable JSON on stdout (exit %d): %v\nstdout: %s\nstderr: %s",
							run.ExitCode, err, run.Stdout, run.Stderr)
					}

					found, failing := 0, 0

					for _, reported := range report.Checks {
						if reported.Severity == "fail" {
							failing++
						}

						if reported.Name != check {
							continue
						}

						found++

						if reported.Severity != tc.severity {
							t.Errorf("%q is %s, want %s: %s", check, reported.Severity, tc.severity, reported.Message)
						}

						for _, want := range tc.message {
							if !strings.Contains(reported.Message, want) {
								t.Errorf("%q says %q, want it to hold %q", check, reported.Message, want)
							}
						}
					}

					if found != 1 {
						t.Errorf("doctor reported %q %d times, want once:\n%s", check, found, run.Stdout)
					}

					// doctor exits 1 exactly when a check fails, so a refused
					// root exits 1. The bare directory fails other checks as
					// well, which is why one manifest is not held to exit 0.
					if report.Failures != failing || (run.ExitCode == 1) != (failing > 0) || (run.ExitCode != 0 && run.ExitCode != 1) {
						t.Errorf("doctor exited %d and counted %d failures with %d failing checks listed", run.ExitCode, report.Failures, failing)
					}
				})
			}
		})
	}
}
