// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRuntimeEvidenceDelegatesVerifiedUploadAndExit(t *testing.T) {
	t.Parallel()

	for _, status := range []int{0, 69} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "forge-ci"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nexit \"$STATUS\"\n"), 0o700))
			cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(reporoot.Path(t), "scripts/ci/runtime-evidence.sh"), "runtime-evidence-full", "evidence path/")

			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "STATUS="+strconv.Itoa(status))

			out, err := cmd.CombinedOutput()
			if status == 0 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}

			require.Equal(t, status, cmd.ProcessState.ExitCode())
			require.Equal(t, []string{"artifact", "upload", "--name", "runtime-evidence-full", "--dir", "evidence path/", "--retention-days", "30", "--if-no-files", "error", "--verify-readback"}, strings.Split(strings.TrimSpace(string(out)), "\n"))
		})
	}
}
