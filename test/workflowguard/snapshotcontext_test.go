// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package workflowguard

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
)

// The transport must delegate restoration to the confined engine operation,
// without doing an ambient shell copy before or afterwards. The application
// package separately tests restore-context's actual confinement and inventory.
func TestSnapshotContextCannotWriteThroughCheckoutSymlink(t *testing.T) {
	t.Parallel()
	workspace, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "snapshot-source", "dist"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "bundle", "files", "dist"), 0o700))
	const original = "outside file must remain intact"
	victim := filepath.Join(outside, "server.jar")
	require.NoError(t, os.WriteFile(victim, []byte(original), 0o600))
	require.NoError(t, os.Symlink(victim, filepath.Join(workspace, "snapshot-source", "dist", "server.jar")))
	sha := strings.Repeat("a", 40)
	plan := []byte(fmt.Sprintf(`{"source_sha":%q}`, sha))
	inventory := []byte(`{"dependencies":[]}`)
	for name, body := range map[string][]byte{
		"snapshot-plan.json":                      plan,
		"bundle/snapshot-plan.json":               plan,
		"bundle/source-sha":                       []byte(sha),
		"bundle/files/snapshot-dependencies.json": inventory,
		"bundle/files/dist/server.jar":            []byte("replacement"),
		"bundle/manifest.sha256":                  []byte(fmt.Sprintf("%x  snapshot-plan.json\n%x  files/snapshot-dependencies.json\n", sha256.Sum256(plan), sha256.Sum256(inventory))),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(workspace, name), body, 0o600))
	}
	command := exec.Command("bash", "-euo", "pipefail", "-c", `run_forge_ci() { printf '%s\n' "$PWD" "$@"; }; source "$1"; snapshot_apply_context bundle snapshot-plan.json`, "snapshot-context-test", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
	command.Dir = workspace
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Equal(t, fmt.Sprintf("%s\ncontainer\nsnapshot-images\nrestore-context\n--plan\n%s\n--input\n%s\n", filepath.Join(workspace, "snapshot-source"), filepath.Join(workspace, "snapshot-plan.json"), filepath.Join(workspace, "bundle")), string(output))
	actual, readErr := os.ReadFile(victim)
	require.NoError(t, readErr)
	require.Equal(t, original, string(actual), "context copy escaped checkout; command error=%v output=%s", err, output)
}
