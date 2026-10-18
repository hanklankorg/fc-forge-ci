// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package report

import (
	"context"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/ghaenv"
)

// kindCase drives one registry kind end-to-end through the real CLI
// command: minimal required flags in, rendered step-summary block out.
type kindCase struct {
	args   []string
	marker string // distinctive header the rendered block must contain
}

// kindCases must cover every kind in the registry — TestKindRegistry
// fails when a kind is added without a row here (and when a row goes
// stale against a removed kind).
func kindCases() map[string]kindCase {
	return map[string]kindCase{
		"build android": {
			args:   []string{"--java-version", "21", "--jdk-dist", "temurin", "--build-module", "app"},
			marker: "## Android Variants Build Summary",
		},
		"build xcode": {
			args:   []string{"--xcode-version", "16.0", "--scheme", "App"},
			marker: "## Xcode Build Summary",
		},
		"publish appstore": {
			args:   []string{"--ipa-file", "App.ipa", "--platform", "ios"},
			marker: "## App Store Connect Upload Summary",
		},
		"publish google-play": {
			args:   []string{"--aab-file", "app.aab", "--package-name", "com.example.app", "--track", "internal", "--status", "completed"},
			marker: "## Google Play Upload Summary",
		},
		"publish maven-central": {
			args:   []string{"--version", "1.2.3"},
			marker: "## Maven Central deployment command completed",
		},
		"publish forge-packages": {
			args:   []string{"--repository", "org/app", "--package-type", "maven", "--registry-name", "GitHub Packages"},
			marker: "## Published to GitHub Packages",
		},
	}
}

// runReport runs the real `report ...` command tree with the given
// subcommand argv.
func runReport(t *testing.T, argv ...string) error {
	t.Helper()

	return New().Run(context.Background(), append([]string{"report"}, argv...))
}

// TestKindRegistry_EveryKindRenders runs each registry kind through the
// real `report <group> <kind>` command with its minimal required flags
// and asserts the kind's summary block lands on the step-summary sink.
func TestKindRegistry_EveryKindRenders(t *testing.T) {
	groups := map[string][]summaryKind{
		"build":   buildKinds(),
		"publish": publishKinds(),
	}

	cases := kindCases()
	seen := make(map[string]bool, len(cases))

	for group, kinds := range groups {
		for _, k := range kinds {
			key := group + " " + k.name

			seen[key] = true

			tc, ok := cases[key]
			if !ok {
				t.Errorf("registry kind %q has no test case — add a row to kindCases", key)

				continue
			}

			t.Run(key, func(t *testing.T) {
				env := ghaenv.Setup(t)

				argv := append([]string{group, k.name}, tc.args...)
				if err := runReport(t, argv...); err != nil {
					t.Fatalf("run %q: %v", key, err)
				}

				body := env.Summary()
				if strings.TrimSpace(body) == "" {
					t.Fatalf("kind %q rendered an empty step summary", key)
				}

				if !strings.Contains(body, tc.marker) {
					t.Errorf("kind %q: summary missing %q in %s", key, tc.marker, body)
				}
			})
		}
	}

	for key := range cases {
		if !seen[key] {
			t.Errorf("test case %q does not match any registry kind — remove or fix the row", key)
		}
	}
}

// TestKindRegistry_RequiredFlagsAreEnforced asserts that EVERY required flag
// of every kind is enforced individually, not just the first one.
//
// It used to run each kind with no flags at all and check the error named
// k.required[0]. That proved only that the loop fires once: with ten kinds
// declaring up to five required flags each, roughly two thirds of them were
// unverified, and an enforcement loop that stopped after the first entry
// would have stayed green.
//
// Supplying every other flag from the kind's own kindCases row and omitting
// one at a time is what makes each flag answer for itself. The rows are
// already maintained for TestKindRegistry_EveryKindRenders, so this reuses
// them rather than duplicating a second fixture table.
func TestKindRegistry_RequiredFlagsAreEnforced(t *testing.T) {
	groups := map[string][]summaryKind{
		"build":   buildKinds(),
		"publish": publishKinds(),
	}

	cases := kindCases()

	for group, kinds := range groups {
		for _, k := range kinds {
			key := group + " " + k.name

			tc, ok := cases[key]
			if !ok {
				continue // TestKindRegistry_EveryKindRenders reports the missing row.
			}

			for _, missing := range k.required {
				t.Run(key+" without --"+missing, func(t *testing.T) {
					ghaenv.Setup(t)

					argv := append([]string{group, k.name}, withoutFlag(tc.args, missing)...)

					err := runReport(t, argv...)
					if err == nil {
						t.Fatalf("kind %q without --%s: expected a required-flag error, got nil", key, missing)
					}

					if !strings.Contains(err.Error(), "--"+missing) {
						t.Errorf("kind %q without --%s: error %q does not name the missing flag", key, missing, err)
					}
				})
			}
		}
	}
}

// withoutFlag returns args with "--name" and its value removed. The kindCases
// rows are flag/value pairs, so dropping two entries removes exactly one flag.
func withoutFlag(args []string, name string) []string {
	out := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		if args[i] == "--"+name {
			i++ // skip the value too

			continue
		}

		out = append(out, args[i])
	}

	return out
}
