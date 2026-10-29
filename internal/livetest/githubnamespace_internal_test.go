// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitHubRuntimeNamespaceRendering(t *testing.T) {
	t.Parallel()
	engine := GitHubEngine{Owner: "Example-Org", Repository: "scratch-engine", Commit: strings.Repeat("a", 40)}
	require.Equal(t, "ghcr.io/example-org", engine.Registry())
	engine.Namespace = "ghcr.io/example-org/scratch-engine"
	for _, flavor := range []string{"base", "full", "java-25", "node-24", "rust-stable", "go-1.27", "android-35"} {
		image := "forge-ci-runtime-" + flavor
		if flavor == "full" {
			image = "forge-ci-runtime"
		}
		text := "uses: diggsweden/forge-ci/.github/workflows/test.yml@main\n  runtime-image: ghcr.io/diggsweden/" + image + ":v0.90.0\n"
		rendered, _, err := RenderGitHubConsumer("test.yml", text, engine, nil)
		require.NoError(t, err)
		require.Contains(t, rendered, engine.Namespace+"/"+image+":"+engine.ImageTag())
		require.Contains(t, rendered, engine.Owner+"/"+engine.Repository+"/.github/workflows/test.yml@"+engine.Commit)
	}
	for _, namespace := range []string{"ghcr.io/example-org/repo:tag", "ghcr.io/user:secret@example-org", "ghcr.io/example-org/repo@sha256:bad", "https://ghcr.io/example-org", "ghcr.io/example-org/"} {
		engine.Namespace = namespace
		_, _, err := RenderGitHubConsumer("test.yml", "", engine, nil)
		require.Error(t, err)
	}
}
