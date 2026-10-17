// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
)

func TestComponentWorkingDirectoryIsLiteralData(t *testing.T) {
	t.Parallel()

	for _, file := range []string{"build-maven.yml", "build-gradle.yml", "build-gradle-android.yml", "publish-maven-central.yml", "publish-forge-packages.yml"} {
		t.Run(file, func(t *testing.T) {
			t.Parallel()
			root := testfs.NewReal(t).Root

			const directory = "project $(touch EXPLOITED)"

			path := filepath.Join(root, directory)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}

			var changeDirectory string

			for _, script := range publisherScripts(t, file) {
				if strings.HasPrefix(script, "cd ") {
					changeDirectory = strings.ReplaceAll(script, "$[[ inputs.working-directory ]]", directory)
				}
			}

			if changeDirectory == "" {
				t.Fatal("component has no working-directory selection")
			}

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-eu", "-c", changeDirectory+"\ntest \"$PWD\" = \"$EXPECTED\"") //nolint:gosec // Actual template directory selection, isolated from all build and publication commands.
			command.Dir = root
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "RC_PROJECT_DIRECTORY=" + directory, "EXPECTED=" + path}
			output, err := command.CombinedOutput()

			if _, statErr := os.Stat(filepath.Join(root, "EXPLOITED")); !os.IsNotExist(statErr) {
				t.Fatal("working-directory input executed a shell command")
			}

			if err != nil {
				t.Fatalf("literal working-directory was not selected: %v\n%s", err, output)
			}
		})
	}
}
