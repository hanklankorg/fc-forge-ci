// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/goccy/go-yaml/ast"
	"github.com/stretchr/testify/require"
)

func TestRuntimeSmokeVersionComesFromTheCurrentContainerfile(t *testing.T) {
	t.Parallel()

	body := reporoot.ReadFile(t, "containers/runtime/Containerfile")

	var expected string

	for line := range strings.SplitSeq(string(body), "\n") {
		if image, ok := strings.CutPrefix(line, "ARG GOLANG_IMAGE="); ok {
			expected, _, _ = strings.Cut(image, "-")
		}
	}

	require.NotEmpty(t, expected)

	w := loadWorkflow(t, ".forgejo/workflows/runtime-images.yml")
	step, ok := w.findStep("Exercise the native runtime toolchain")
	require.True(t, ok)
	literal, ok := mappingValue(step, "run").(*ast.LiteralNode)
	require.True(t, ok)

	root := t.TempDir()
	for _, directory := range []string{"containers/runtime", "scripts/ci"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, directory), 0o700))
	}

	require.NoError(t, os.WriteFile(filepath.Join(root, "containers/runtime/Containerfile"), body, 0o600))
	// Execute the real workflow extraction, but replace every smoke operation
	// with a parameter oracle. No tool installation, engine or registry is used.
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts/ci/runtime-smoke.sh"), []byte("set -eu\ntest \"$1\" = go\nprintf '%s' \"${2:?missing source Go version}\"\n"), 0o600))
	cmd := exec.CommandContext(t.Context(), testBash(t), "-c", literal.Value.Value)
	cmd.Dir = root

	cmd.Env = append(os.Environ(), "TARGET=go")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Equal(t, expected, string(output))
}
