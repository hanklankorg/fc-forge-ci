// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestKanikoActionBuildPlanRemovesHostPathsAndRejectsUnsupportedInputs(t *testing.T) {
	t.Parallel()

	for _, change := range []string{"valid", "secret", "unknown field", "structured value", "load mode", "context escape", "absolute file", "symlink escape"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(workspace, "source"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "source", "Containerfile"), []byte("FROM scratch\n"), 0o600))

			build := map[string]any{"mode": "oci", "context": ".", "file": "Containerfile", "secrets": "", "auth-file": "/host/private/config.json", "output-dir": "/host/private/layout", "labels": "label=spaces and commas, preserved"}

			switch change {
			case "secret":
				build["secrets"] = "id=private,src=/host/private/secret"
			case "unknown field":
				build["registry-password"] = "must-not-be-baked"
			case "structured value":
				build["build-args"] = []string{"invalid"}
			case "load mode":
				build["mode"] = "load"
			case "context escape":
				build["context"] = ".."
			case "absolute file":
				build["file"] = "/host/Containerfile"
			case "symlink escape":
				require.NoError(t, os.Symlink(workspace, filepath.Join(workspace, "source", "escape")))

				build["context"] = "escape"
			}

			body, err := json.Marshal(map[string]any{"container build": build})
			require.NoError(t, err)
			planPath := filepath.Join(t.TempDir(), "plan.json")
			require.NoError(t, os.WriteFile(planPath, body, 0o600))

			got, err := kanikoActionBuildPlan(workspace, planPath)
			if change != "valid" {
				require.ErrorIs(t, err, errs.ErrValidation)
				require.Nil(t, got)

				return
			}

			require.NoError(t, err)
			require.NotContains(t, string(got), "/host/")
			require.Contains(t, string(got), "spaces and commas, preserved")
			require.Equal(t, "/host/private/config.json", build["auth-file"], "preparation must not mutate the caller's plan")
		})
	}
}
