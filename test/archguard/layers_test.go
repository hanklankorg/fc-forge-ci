// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archguard

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const internalPrefix = "github.com/diggsweden/forge-ci/internal/"

// layerOf classifies an internal package path (given relative to internal/)
// into its hexagonal layer (ADR 0004). The import direction between layers is
// enforced by the depguard layer-* rules in .golangci.yml; the guards here use
// the classification for their own placement rules. Test infrastructure has an
// explicit separate layer. listval in particular is imported by domain itself,
// which is exactly why it cannot live inside one of the layered trees.
func layerOf(rel string) string {
	for _, layer := range []string{"domain", "adapters", "app", "cli"} {
		if rel == layer || strings.HasPrefix(rel, layer+"/") {
			return layer
		}
	}

	for _, prefix := range []string{"testutil", "livetest"} {
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return "test"
		}
	}

	return "utility"
}

// TestLayerOf_ClassifiesEveryTreeIntoItsLayer pins the classifier the
// placement guards are stated in terms of. Everything under internal/ has to
// land somewhere, and the fallback is "utility", which is the strictest inward
// layer after domain, so a path that fell through by accident is held to the
// utility rules rather than exempted. The depguard utility rule mirrors this
// with negated globs, so a new package is covered without being listed.
func TestLayerOf_ClassifiesEveryTreeIntoItsLayer(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]string{
		"domain":                "domain",
		"domain/pipeline":       "domain",
		"adapters":              "adapters",
		"adapters/gitlab":       "adapters",
		"app":                   "app",
		"app/build":             "app",
		"cli":                   "cli",
		"cli/commands/platform": "cli",
		"testutil":              "test",
		"testutil/toolrecorder": "test",
		"livetest":              "test",
		"livetest/conformance":  "test",
		"pathsafe":              "utility",
		"safeexec":              "utility",
		"listval":               "utility",
		"domainish":             "utility",
		"applesauce":            "utility",
		"clip":                  "utility",
		"adaptersomething":      "utility",
		"testutilities":         "utility",
	} {
		require.Equalf(t, want, layerOf(path), "layerOf(%q)", path)
	}
}
