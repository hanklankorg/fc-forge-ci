// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package artifact_test

import (
	"context"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli/commands/artifact"
)

// A release job sets ARTIFACT_NAME (and ARTIFACT_DIR) as project settings for
// other commands. A download by pattern in the same job must not pick the name
// up and then refuse "--name or --pattern", nor move its --dir.
func TestDownloadIgnoresAmbientProjectSettings(t *testing.T) {
	t.Setenv("ARTIFACT_NAME", "hello-cargo-container")
	t.Setenv("ARTIFACT_DIR", "artifacts")
	t.Setenv("ARTIFACT_PATTERN", "ambient-*")

	root := artifact.New()

	var name, pattern, dir string

	for _, sub := range root.Commands {
		if sub.Name == "download" {
			sub.Action = func(_ context.Context, cmd *cli.Command) error {
				name, pattern, dir = cmd.String("name"), cmd.String("pattern"), cmd.String("dir")

				return nil
			}
		}
	}

	err := root.Run(t.Context(), []string{"artifact", "download", "--pattern", "release-binary-*", "--merge-multiple"})
	if err != nil {
		t.Fatal(err)
	}

	if name != "" || pattern != "release-binary-*" || dir != "." {
		t.Errorf("download read the job env: --name %q --pattern %q --dir %q; want \"\", \"release-binary-*\", \".\"",
			name, pattern, dir)
	}
}

// The transfer verbs mirror upload-artifact/download-artifact inputs, which a
// step states explicitly. Only the run, the repository and the runner's temp
// directory, which describe the run, may come from the environment.
func TestTransferFlagsReadNoEnvironment(t *testing.T) {
	t.Parallel()

	runContext := map[string]bool{"download --run-id": true, "download --repository": true, "upload --temp-dir": true}

	for _, sub := range artifact.New().Commands {
		for _, flag := range sub.Flags {
			site := sub.Name + " --" + flag.Names()[0]

			sourced, ok := flag.(interface{ GetEnvVars() []string })
			if !ok {
				t.Fatalf("%s: cannot read its env sources", site)
			}

			if envs := sourced.GetEnvVars(); len(envs) > 0 && !runContext[site] {
				t.Errorf("artifact %s reads %v from the environment; pass it as a flag instead", site, envs)
			}
		}
	}
}
