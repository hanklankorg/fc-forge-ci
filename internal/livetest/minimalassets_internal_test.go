// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestMinimalSourceCarriesCompilerDeclaredPolicyAsset(t *testing.T) {
	paths, err := minimalEmbeddedAssets(t.Context(), reporoot.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(paths, filepath.Join("internal", "domain", "enforcement", "policy.json")) {
		t.Fatalf("minimal source drops the CLI's embedded administrator policy: %v", paths)
	}
}
