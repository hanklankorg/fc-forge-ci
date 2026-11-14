// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package deps_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestFromCmd_JSONSeparatesRunnerSummaries(t *testing.T) {
	for _, runner := range []string{"local", "github", "gitlab", "forgejo"} {
		for _, file := range []bool{false, true} {
			t.Run(runner+" summary-file="+strconv.FormatBool(file), func(t *testing.T) {
				env := testenv.New(t)
				env.Setenv("FORGE_CI_PROVIDER", "local")
				env.Setenv("FORGE_CI_RUNNER", runner)
				path := env.Path("summary.md")

				if file {
					for _, name := range []string{"GITHUB_STEP_SUMMARY", "FORGEJO_STEP_SUMMARY", "CI_SUMMARY_FILE"} {
						env.Setenv(name, path)
					}
				}

				stdout, err := os.OpenFile(env.Path("stdout.json"), os.O_CREATE|os.O_WRONLY, 0o600)
				require.NoError(t, err)

				previous := os.Stdout
				os.Stdout = stdout

				t.Cleanup(func() {
					os.Stdout = previous

					require.NoError(t, stdout.Close())
				})

				var stderr bytes.Buffer

				command := &cli.Command{
					Name: "fixture", Writer: stdout, ErrWriter: &stderr,
					Flags: []cli.Flag{&cli.BoolFlag{Name: "json", Value: true}},
				}
				ctx := t.Context()
				require.NoError(t, deps.FromCmd(ctx, command, func(d *deps.Deps) error {
					require.NoError(t, d.OutputSink.Set(ctx, "config-plan-json", `{"version":1}`))

					return d.SummarySink.Append(ctx, "## Config summary\n")
				}))

				body, err := os.ReadFile(stdout.Name())
				require.NoError(t, err)

				var decoded map[string]string

				require.NoError(t, json.Unmarshal(body, &decoded), "stdout must contain exactly one JSON document")
				require.Equal(t, map[string]string{"config-plan-json": `{"version":1}`}, decoded)

				logsSummary := runner == "gitlab" || (runner == "forgejo" && !file)
				if logsSummary {
					require.Contains(t, stderr.String(), "## Config summary")
				} else {
					require.Empty(t, stderr.String())
				}

				if file && runner != "local" {
					body, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, "## Config summary\n", string(body))
				} else {
					require.NoFileExists(t, path)
				}
			})
		}
	}
}

func TestBuild_RunnerSinkPolicy(t *testing.T) {
	for _, tc := range []struct {
		name         string
		target       provider.ForgeAPI
		marker       string
		actions      bool
		nativeOutput bool
		summaryFile  bool
		wantOutput   string
		wantSummary  string
	}{
		{"forgejo native file", provider.ForgeGitLab, "FORGEJO_ACTIONS", true, true, true, "FORGEJO_OUTPUT", "GITHUB_STEP_SUMMARY"},
		{"forgejo native log", provider.ForgeGitHub, "FORGEJO_ACTIONS", true, true, false, "FORGEJO_OUTPUT", "stdout"},
		{"gitea alias file", provider.ForgeLocal, "GITEA_ACTIONS", true, false, true, "GITHUB_OUTPUT", "GITHUB_STEP_SUMMARY"},
		{"forgejo alias log", provider.ForgeForgejo, "FORGEJO_ACTIONS", true, false, false, "GITHUB_OUTPUT", "stdout"},
		{"github cross forge file", provider.ForgeForgejo, "", true, false, true, "GITHUB_OUTPUT", "GITHUB_STEP_SUMMARY"},
		{"github no summary", provider.ForgeForgejo, "", true, false, false, "GITHUB_OUTPUT", ""},
		{"outside runner ignores files", provider.ForgeForgejo, "FORGEJO_ACTIONS", false, true, true, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testenv.New(t)

			paths := map[string]string{}
			for _, key := range []string{"FORGEJO_OUTPUT", "GITHUB_OUTPUT", "CI_OUTPUT", "GITHUB_STEP_SUMMARY", "CI_SUMMARY_FILE", "stdout"} {
				paths[key] = env.Path(key)
				require.NoError(t, os.WriteFile(paths[key], []byte(key+" canary\n"), 0o600))

				if key != "stdout" {
					t.Setenv(key, paths[key])
				}
			}

			if !tc.nativeOutput {
				t.Setenv("FORGEJO_OUTPUT", "")
			}

			if !tc.summaryFile {
				t.Setenv("GITHUB_STEP_SUMMARY", "")
			}

			t.Setenv("FORGE_CI_PROVIDER", string(tc.target))
			t.Setenv("GITHUB_ACTIONS", boolEnv(tc.actions))

			if tc.marker != "" {
				t.Setenv(tc.marker, "true")
			}
			// Target metadata must not select the runner's output dialect.
			t.Setenv("FORGEJO_SERVER_URL", "https://target.invalid")
			t.Setenv("FORGEJO_REPOSITORY", "target/repo")
			t.Setenv("CI_RESULTS_DIR", env.Path("results"))

			logFile, err := os.OpenFile(paths["stdout"], os.O_WRONLY|os.O_APPEND, 0o600)
			require.NoError(t, err)
			// Build captures os.Stdout for Forgejo's real summary fallback.
			// A regular owned file avoids pipes, goroutines, and subprocesses.
			previousStdout := os.Stdout
			os.Stdout = logFile

			t.Cleanup(func() {
				os.Stdout = previousStdout

				require.NoError(t, logFile.Close())
			})

			ctx := context.Background()
			built, err := deps.Build(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, built.Close(ctx)) })
			require.Equal(t, tc.target, built.Platform)
			require.Equal(t, tc.target, built.Provider.Name())
			require.NoError(t, built.OutputSink.Set(ctx, "version-no-v", "1.2.3"))
			require.NoError(t, built.OutputSink.SetBool(ctx, "published", false))
			require.NoError(t, built.SummarySink.Append(ctx, "## Build\n"))
			require.NoError(t, built.SummarySink.Append(ctx, "Complete\n"))
			require.NoError(t, built.Close(ctx))
			require.ErrorContains(t, built.OutputSink.Set(ctx, "late", "must-not-write"), "output sink is closed")
			require.ErrorContains(t, built.OutputSink.SetMultiline(ctx, "late", []string{"must-not-write"}), "output sink is closed")
			require.NoError(t, built.Close(ctx))

			for key, path := range paths {
				body, readErr := os.ReadFile(path)
				require.NoError(t, readErr)

				want := key + " canary\n"
				if key == tc.wantOutput {
					want += "version-no-v=1.2.3\npublished=false\n"
				}

				if key == tc.wantSummary {
					want += "## Build\nComplete\n"
				}

				require.Equal(t, want, string(body), key)
			}

			require.NoDirExists(t, env.Path("results"))
		})
	}
}
