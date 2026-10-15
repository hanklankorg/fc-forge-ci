// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	rootcli "github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestProvenanceRoot_GenerateOnly(t *testing.T) {
	for _, tc := range []struct {
		name, runner, repo, ref, sha, builder, invocation string
		missing                                           string
		legacy                                            bool
	}{
		{
			name: "github", runner: "github", repo: "https://github.invalid/gh/source", ref: "v1.2.3",
			sha:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			builder:    "https://github.invalid/gh/source/.github/workflows/build.yml@refs/tags/v1.2.3",
			invocation: "https://github.invalid/gh/source/actions/runs/11",
		},
		{
			name: "forgejo", runner: "forgejo", repo: "https://forgejo.invalid/fj/source", ref: "v4.5.6",
			sha:        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			builder:    "https://forgejo.invalid/fj/source/.forgejo/workflows/build.yml@refs/tags/v4.5.6",
			invocation: "https://forgejo.invalid/fj/source/actions/runs/22",
		},
		{
			name: "gitlab", runner: "gitlab", repo: "https://gitlab.invalid/gl/source", ref: "v7.8.9",
			sha:        "cccccccccccccccccccccccccccccccccccccccc",
			builder:    "https://gitlab.invalid/gl/source/-/jobs/33",
			invocation: "https://gitlab.invalid/gl/source/-/jobs/33",
		},
		{
			name: "compatibility aliases", runner: "aliases", repo: "https://github.invalid/gh/source", ref: "v1.2.3",
			sha:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			builder:    "https://github.invalid/gh/source/.github/workflows/build.yml@refs/tags/v1.2.3",
			invocation: "https://github.invalid/gh/source/actions/runs/11",
		},
		{name: "local refuses unknown source", runner: "local"},
		{name: "missing runner repository", runner: "github", missing: "GITHUB_REPOSITORY"},
		{
			name: "legacy native preserved", runner: "forgejo", legacy: true,
			repo: "https://forgejo.invalid/fj/source", ref: "v4.5.6", sha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			builder:    "https://forgejo.invalid/fj/source/.forgejo/workflows/legacy.yml@v4.5.6",
			invocation: "https://forgejo.invalid/fj/source/actions/runs/22",
		},
		{
			// Preservation of the shipped shape is not a cross-forge trust policy.
			name: "legacy cross forge preserved", runner: "github", legacy: true,
			repo: "https://forgejo.invalid/fj/source", ref: "v4.5.6", sha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			builder:    "https://forgejo.invalid/fj/source/.forgejo/workflows/legacy.yml@v4.5.6",
			invocation: "https://github.invalid/gh/source/actions/runs/11",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			targets := []string{"github", "forgejo", "gitlab", "local"}

			var profileArgs []string

			if tc.legacy {
				targets = []string{"forgejo"}
				profileArgs = []string{"--profile", "forgejo-actions", "--workflow", "legacy.yml"}
			}

			for _, target := range targets {
				t.Run(target, func(t *testing.T) {
					env := provenanceRootEnv(t, tc.runner)
					if tc.missing != "" {
						t.Setenv(tc.missing, "")
					}

					const checksums = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd  app.tgz\n"
					require.NoError(t, os.WriteFile(env.Path("checksums"), []byte(checksums), 0o600))
					require.NoError(t, os.WriteFile(env.Path("statement.json"), []byte("output canary\n"), 0o600))
					args := append([]string{"forge-ci", "--provider", target, "--format", "text", "release", "provenance",
						"--checksums-file", env.Path("checksums"), "--output", env.Path("statement.json"),
						"--started-on", "2026-01-02T03:04:05Z", "--go-sum", ""}, profileArgs...)
					command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
					command.Writer, command.ErrWriter = os.Stdout, os.Stderr
					runErr := command.Run(t.Context(), args)
					body, err := os.ReadFile(env.Path("statement.json"))
					require.NoError(t, err)

					if tc.repo == "" {
						require.ErrorIs(t, runErr, errs.ErrMissingInput)
						require.ErrorContains(t, runErr, "runner repository URL is unknown")
						require.Equal(t, "output canary\n", string(body))
					} else {
						require.NoError(t, runErr)

						ext := fmt.Sprintf(`{"source":%q,"ref":%q}`, "git+"+tc.repo, tc.ref)
						buildType, internal := "https://diggsweden.github.io/forge-ci/release-build/v1", "{}"

						if tc.legacy {
							ext = fmt.Sprintf(`{"workflow":{"repository":%q,"ref":%q,"path":".forgejo/workflows/legacy.yml"}}`, tc.repo, tc.ref)
							buildType, internal = "https://forgejo.org/actions/buildtypes/workflow/v1", `{"runner":"forgejo-actions"}`
						}

						want := fmt.Sprintf(`{
							"_type":"https://in-toto.io/Statement/v1",
							"subject":[{"name":"app.tgz","digest":{"sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}}],
							"predicateType":"https://slsa.dev/provenance/v1",
							"predicate":{
								"buildDefinition":{"buildType":%q,"externalParameters":%s,"internalParameters":%s,
									"resolvedDependencies":[{"uri":%q,"digest":{"gitCommit":%q}}]},
								"runDetails":{"builder":{"id":%q},"metadata":{"invocationId":%q,
									"startedOn":"2026-01-02T03:04:05Z","finishedOn":"2026-01-02T03:04:05Z"}}
							}}`, buildType, ext, internal, "git+"+tc.repo+"@"+tc.ref, tc.sha, tc.builder, tc.invocation)
						require.JSONEq(t, want, string(body))
					}

					for name, want := range map[string]string{"checksums": checksums, "command-log": "", "GITHUB_OUTPUT": "sink canary\n", "GITHUB_STEP_SUMMARY": "sink canary\n", "CI_OUTPUT": "sink canary\n", "CI_SUMMARY_FILE": "sink canary\n"} {
						got, readErr := os.ReadFile(env.Path(name))
						require.NoError(t, readErr)
						require.Equal(t, want, string(got), name)
					}

					require.NoFileExists(t, env.Path("statement.json.bundle"))
					require.NoDirExists(t, env.Path("results"))
				})
			}
		})
	}
}

func TestProvenanceRootEnv_RestoresLogging(t *testing.T) {
	for _, mode := range []string{"existing default", "custom default"} {
		t.Run(mode, func(t *testing.T) {
			originalLogger := slog.Default()
			originalWriter, originalFlags := log.Writer(), log.Flags()

			t.Cleanup(func() {
				slog.SetDefault(originalLogger)
				log.SetOutput(originalWriter)
				log.SetFlags(originalFlags)
			})

			var structured, standard bytes.Buffer
			if mode == "custom default" {
				slog.SetDefault(slog.New(slog.NewTextHandler(&structured, nil)))
			}

			logger := slog.Default()

			log.SetOutput(&standard)
			log.SetFlags(log.Lmsgprefix)

			stdout, stderr := os.Stdout, os.Stderr

			var commandLog *os.File

			t.Run("fixture", func(t *testing.T) {
				env := provenanceRootEnv(t, "github")
				commandLog = os.Stderr
				command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
				command.Writer, command.ErrWriter = os.Stdout, os.Stderr
				// Root logger setup runs before this owned missing-input refusal.
				err := command.Run(t.Context(), []string{"forge-ci", "--provider", "forgejo", "--format", "text", "release", "provenance",
					"--checksums-file", env.Path("missing-checksums"), "--output", env.Path("statement.json"),
					"--started-on", "2026-01-02T03:04:05Z", "--go-sum", ""})
				require.ErrorIs(t, err, os.ErrNotExist)
			})

			require.NotNil(t, commandLog)
			_, err := commandLog.Stat()
			require.ErrorIs(t, err, os.ErrClosed)
			require.Same(t, stdout, os.Stdout)
			require.Same(t, stderr, os.Stderr)
			require.Same(t, logger, slog.Default())
			require.Same(t, &standard, log.Writer())
			require.Equal(t, log.Lmsgprefix, log.Flags())
			log.Print("after fixture cleanup")
			require.Equal(t, log.Prefix()+"after fixture cleanup\n", standard.String())
			require.Empty(t, structured.String())
		})
	}
}

func provenanceRootEnv(t *testing.T, runner string) *testenv.Env {
	t.Helper()
	env := testenv.New(t)
	t.Chdir(env.Temp)
	t.Setenv("PATH", env.MkdirAll("empty-path"))

	for key, value := range map[string]string{
		"FORGE_CI_PLAN": "", "FORGE_CI_PROVIDER": "", "FORGE_CI_RUNNER": "",
		"SIGN_METHOD": "", "SIGN_KEY": "", "SLSA_EXTERNAL_PARAMETERS_JSON": "",
		"SOURCE_DATE_EPOCH": "invalid-unused-epoch",
		"GITHUB_SERVER_URL": "https://github.invalid///", "GITHUB_REPOSITORY": "gh/source", "GITHUB_REF_NAME": "v1.2.3", "GITHUB_SHA": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"GITHUB_WORKFLOW_REF": "gh/source/.github/workflows/build.yml@refs/tags/v1.2.3", "GITHUB_WORKFLOW": "Wrong display name", "GITHUB_RUN_ID": "11",
		"FORGEJO_SERVER_URL": "https://forgejo.invalid///", "FORGEJO_REPOSITORY": "fj/source", "FORGEJO_REF_NAME": "v4.5.6", "FORGEJO_SHA": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"FORGEJO_WORKFLOW_REF": "fj/source/.forgejo/workflows/build.yml@refs/tags/v4.5.6", "FORGEJO_RUN_ID": "22",
		"CI_SERVER_URL": "https://gitlab.invalid///", "CI_PROJECT_PATH": "gl/source", "CI_PROJECT_URL": "https://gitlab.invalid/gl/source", "CI_COMMIT_REF_NAME": "v7.8.9", "CI_COMMIT_SHA": "cccccccccccccccccccccccccccccccccccccccc",
		"CI_JOB_URL": "https://gitlab.invalid/gl/source/-/jobs/33", "CI_PIPELINE_URL": "https://gitlab.invalid/gl/source/-/pipelines/44",
		"REPOSITORY": "neutral/wrong", "CI_REPO": "neutral/wrong", "REF_NAME": "wrong-neutral-ref", "CI_REF_NAME": "wrong-ci-ref",
		"CI_COMMIT": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "COMMIT_SHA": "ffffffffffffffffffffffffffffffffffffffff",
		"FORGEJO_SERVER": "https://alias.invalid", "FORGEJO_REPO": "alias/wrong", "CI_RUN_ID": "wrong-neutral-run",
	} {
		t.Setenv(key, value)
	}

	switch runner {
	case "github":
		t.Setenv("GITHUB_ACTIONS", "true")
	case "forgejo":
		t.Setenv("GITHUB_ACTIONS", "true")
		t.Setenv("FORGEJO_ACTIONS", "true")
	case "aliases":
		t.Setenv("GITHUB_ACTIONS", "true")
		t.Setenv("GITEA_ACTIONS", "true")

		for _, key := range []string{"FORGEJO_SERVER_URL", "FORGEJO_REPOSITORY", "FORGEJO_REF_NAME", "FORGEJO_SHA", "FORGEJO_WORKFLOW_REF", "FORGEJO_RUN_ID"} {
			t.Setenv(key, "")
		}
	case "gitlab":
		t.Setenv("GITLAB_CI", "true")
	}

	for _, key := range []string{"GITHUB_OUTPUT", "GITHUB_STEP_SUMMARY", "CI_OUTPUT", "CI_SUMMARY_FILE"} {
		t.Setenv(key, env.Path(key))
		require.NoError(t, os.WriteFile(env.Path(key), []byte("sink canary\n"), 0o600))
	}

	t.Setenv("CI_RESULTS_DIR", env.Path("results"))

	logFile, err := os.Create(env.Path("command-log"))
	require.NoError(t, err)

	stdout, stderr, logger := os.Stdout, os.Stderr, slog.Default()
	standardWriter, standardFlags := log.Writer(), log.Flags()
	os.Stdout, os.Stderr = logFile, logFile //nolint:reassign // capture the real root command's process streams in this serial test.

	t.Cleanup(func() {
		os.Stdout, os.Stderr = stdout, stderr //nolint:reassign // restore process streams before closing the owned file.

		slog.SetDefault(logger)
		// SetDefault can rewire the standard logger, so restore it last.
		log.SetOutput(standardWriter)
		log.SetFlags(standardFlags)
		require.NoError(t, logFile.Close())
	})

	return env
}

// provenanceManifestDist writes a dist directory holding one asset, its
// checksums file and the release file manifest classifying both, and returns
// the checksums file's path relative to the working directory.
func provenanceManifestDist(t *testing.T, env *testenv.Env, recordedDigest string) string {
	t.Helper()

	env.MkdirAll("dist")
	require.NoError(t, os.WriteFile(env.Path("dist/app.tgz"), []byte("archive"), 0o600))
	require.NoError(t, os.WriteFile(env.Path("dist/SHA256SUMS"), []byte(recordedDigest+"  app.tgz\n"), 0o600))
	require.NoError(t, os.WriteFile(env.Path("dist/release-files.json"), []byte(`{
  "version": 1,
  "assets": [
    {"path": "dist/app.tgz", "name": "app.tgz", "source": "producer"},
    {"path": "dist/SHA256SUMS", "name": "SHA256SUMS", "source": "producer"}
  ],
  "checksums": [{"path": "dist/SHA256SUMS", "name": "SHA256SUMS", "source": "producer"}],
  "sboms": [],
  "evidence": [],
  "provenance": []
}
`), 0o600))

	return "dist/SHA256SUMS"
}

func runProvenanceRoot(t *testing.T, args ...string) error {
	t.Helper()

	command := rootcli.New(rootcli.BuildInfo{Version: "dev"})
	command.Writer, command.ErrWriter = os.Stdout, os.Stderr

	return command.Run(t.Context(), append([]string{"forge-ci", "--provider", "forgejo", "--format", "text", "release", "provenance",
		"--started-on", "2026-01-02T03:04:05Z", "--go-sum", ""}, args...))
}

// The manifest only selects and validates the checksums file. The statement
// built from it is the statement built from that same file named directly.
func TestProvenanceRoot_ChecksumsFromManifestWritesTheSameStatement(t *testing.T) {
	for name, profileArgs := range map[string][]string{
		"generic":         nil,
		"forgejo-actions": {"--profile", "forgejo-actions", "--workflow", "release.yml"},
	} {
		t.Run(name, func(t *testing.T) {
			env := provenanceRootEnv(t, "forgejo")
			checksumFile := provenanceManifestDist(t, env, fmt.Sprintf("%x", sha256.Sum256([]byte("archive"))))

			require.NoError(t, runProvenanceRoot(t, append([]string{"--checksums-file", checksumFile, "--output", "direct.json"}, profileArgs...)...))
			require.NoError(t, runProvenanceRoot(t, append([]string{"--checksums-from-manifest", "--output", "manifest.json"}, profileArgs...)...))

			direct, err := os.ReadFile(env.Path("direct.json"))
			require.NoError(t, err)
			require.Contains(t, string(direct), `"app.tgz"`)

			fromManifest, err := os.ReadFile(env.Path("manifest.json"))
			require.NoError(t, err)
			require.Equal(t, string(direct), string(fromManifest))
		})
	}
}

func TestProvenanceRoot_ChecksumsFromManifestRefusesBeforeWritingTheStatement(t *testing.T) {
	archiveDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("archive")))

	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, env *testenv.Env)
		args    []string
		want    error
		message string
	}{
		{
			name: "digest mismatch",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				provenanceManifestDist(t, env, strings.Repeat("d", 64))
			},
			want: errs.ErrValidation, message: "checksum digest mismatch for app.tgz",
		},
		{
			// With no manifest the checksums file is discovered under the
			// dist directory, and an empty one holds none.
			name: "no manifest and nothing to discover",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				env.MkdirAll("dist")
			},
			want: errs.ErrValidation, message: "expected exactly one checksums file in dist, found 0",
		},
		{
			name: "linked checksums file",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()

				checksumFile := provenanceManifestDist(t, env, archiveDigest)
				require.NoError(t, os.Rename(env.Path(checksumFile), env.Path("outside")))
				require.NoError(t, os.Symlink(env.Path("outside"), env.Path(checksumFile)))
			},
			want: errs.ErrValidation, message: "symlink",
		},
		{
			// Naming the file the manifest names is still naming a file.
			name: "explicit checksum file",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				provenanceManifestDist(t, env, archiveDigest)
			},
			args: []string{"--checksums-file", "dist/SHA256SUMS"},
			want: errs.ErrInvalidConfig, message: `--checksums-from-manifest conflicts with the explicit checksums file "dist/SHA256SUMS"`,
		},
		{
			// The flag's default value, given on the command line.
			name: "explicit stdin",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				provenanceManifestDist(t, env, archiveDigest)
			},
			args: []string{"--checksums-file", "-"},
			want: errs.ErrInvalidConfig, message: `--checksums-from-manifest conflicts with the explicit checksums file "-"`,
		},
		{
			name: "checksum file variable",
			arrange: func(t *testing.T, env *testenv.Env) {
				t.Helper()
				provenanceManifestDist(t, env, archiveDigest)
				t.Setenv("CHECKSUMS_FILE", "dist/SHA256SUMS")
			},
			want: errs.ErrInvalidConfig, message: `--checksums-from-manifest conflicts with the explicit checksums file "dist/SHA256SUMS"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := provenanceRootEnv(t, "forgejo")
			tc.arrange(t, env)

			err := runProvenanceRoot(t, append([]string{"--checksums-from-manifest", "--output", "statement.json"}, tc.args...)...)
			require.ErrorIs(t, err, tc.want)
			require.ErrorContains(t, err, tc.message)
			require.NoFileExists(t, env.Path("statement.json"))
		})
	}
}

// A checksums-file variable that is set but empty names no file, so it does not
// conflict with the manifest's.
func TestProvenanceRoot_ChecksumsFromManifestIgnoresAnEmptyChecksumFileVariable(t *testing.T) {
	env := provenanceRootEnv(t, "forgejo")
	provenanceManifestDist(t, env, fmt.Sprintf("%x", sha256.Sum256([]byte("archive"))))
	t.Setenv("CHECKSUMS_FILE", "")

	require.NoError(t, runProvenanceRoot(t, "--checksums-from-manifest", "--output", "statement.json"))
	require.FileExists(t, env.Path("statement.json"))
}

// A job triggered by a release request runs on the request's ref and commit
// while its statement names the release tag and the release commit.
// --ref-name and --source-sha on the runner's own environment write the bytes
// that an environment naming the tag and the commit writes without them.
func TestProvenanceRoot_SourceFlagsWriteTheStatementOfTheOverriddenEnvironment(t *testing.T) {
	const (
		requestRef = "release-request/v9.9.9"
		requestSHA = "1111111111111111111111111111111111111111"
		releaseTag = "v9.9.9"
		releaseSHA = "2222222222222222222222222222222222222222"
	)

	for name, profileArgs := range map[string][]string{
		"generic":         nil,
		"forgejo-actions": {"--profile", "forgejo-actions", "--workflow", "release.yml"},
	} {
		t.Run(name, func(t *testing.T) {
			env := provenanceRootEnv(t, "forgejo")
			args := append([]string{"--checksums-file", provenanceManifestDist(t, env, strings.Repeat("d", 64))}, profileArgs...)

			// The runner's own view: both namespaces name the request.
			for key, value := range map[string]string{
				"FORGEJO_REF_NAME": requestRef, "FORGEJO_REF_TYPE": "tag", "FORGEJO_SHA": requestSHA,
				"GITHUB_SERVER_URL": os.Getenv("FORGEJO_SERVER_URL"), "GITHUB_REPOSITORY": os.Getenv("FORGEJO_REPOSITORY"),
				"GITHUB_REF_NAME": requestRef, "GITHUB_REF_TYPE": "tag", "GITHUB_SHA": requestSHA,
			} {
				t.Setenv(key, value)
			}

			require.NoError(t, runProvenanceRoot(t, append([]string{"--output", "flags.json", "--ref-name", releaseTag, "--source-sha", releaseSHA}, args...)...))

			// The ten overrides: the server and repository restate the
			// runner's own values, the ref and commit name the release.
			for _, prefix := range []string{"FORGEJO_", "GITHUB_"} {
				t.Setenv(prefix+"SERVER_URL", os.Getenv("FORGEJO_SERVER_URL"))
				t.Setenv(prefix+"REPOSITORY", os.Getenv("FORGEJO_REPOSITORY"))
				t.Setenv(prefix+"REF_NAME", releaseTag)
				t.Setenv(prefix+"REF_TYPE", "tag")
				t.Setenv(prefix+"SHA", releaseSHA)
			}

			require.NoError(t, runProvenanceRoot(t, append([]string{"--output", "overrides.json"}, args...)...))

			overrides, err := os.ReadFile(env.Path("overrides.json"))
			require.NoError(t, err)
			require.Contains(t, string(overrides), "@"+releaseTag+`"`)
			require.Contains(t, string(overrides), releaseSHA)
			require.NotContains(t, string(overrides), requestSHA)

			flags, err := os.ReadFile(env.Path("flags.json"))
			require.NoError(t, err)
			require.Equal(t, string(overrides), string(flags))
		})
	}
}

// A source flag that is set never falls back to the runner's ref or commit.
func TestProvenanceRoot_RefusesAnInvalidSourceFlagBeforeWritingTheStatement(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string
		want    error
		message string
	}{
		"empty ref name":     {args: []string{"--ref-name", ""}, want: errs.ErrUsage, message: `source ref name "" is not a git ref name`},
		"ref expression":     {args: []string{"--ref-name", "v1.2.3~1"}, want: errs.ErrUsage, message: "is not a git ref name"},
		"empty source sha":   {args: []string{"--source-sha", ""}, want: errs.ErrValidation, message: "commit SHA must be"},
		"abbreviated commit": {args: []string{"--source-sha", "2222222"}, want: errs.ErrValidation, message: "commit SHA must be"},
	} {
		t.Run(name, func(t *testing.T) {
			env := provenanceRootEnv(t, "forgejo")
			checksumFile := provenanceManifestDist(t, env, strings.Repeat("d", 64))

			err := runProvenanceRoot(t, append([]string{"--checksums-file", checksumFile, "--output", "statement.json"}, tc.args...)...)
			require.ErrorIs(t, err, tc.want)
			require.ErrorContains(t, err, tc.message)
			require.NoFileExists(t, env.Path("statement.json"))
		})
	}
}

// envKeyCosign stands in for cosign with shell builtins only, as the test's
// PATH holds nothing else. It refuses to sign unless the named key arrived and
// the forge token did not.
const envKeyCosign = `#!/bin/sh
[ "${PROVENANCE_KEY:-}" = key-material ] || { echo 'the signing key did not arrive' >&2; exit 3; }
[ -z "${FORGEJO_TOKEN+set}" ] || { echo 'the forge token reached cosign' >&2; exit 4; }
`

// Signing the statement with an env:// key hands cosign that key and not the
// job's other secrets.
func TestProvenanceRoot_EnvKeySigningHidesOtherSecretsFromCosign(t *testing.T) {
	env := provenanceRootEnv(t, "forgejo")
	checksumFile := provenanceManifestDist(t, env, fmt.Sprintf("%x", sha256.Sum256([]byte("archive"))))

	require.NoError(t, os.WriteFile(env.Path("empty-path", "cosign"), []byte(envKeyCosign), 0o700)) //nolint:gosec // G306: the fake signer must be executable.

	t.Setenv("PROVENANCE_KEY", "key-material")
	t.Setenv("FORGEJO_TOKEN", "forge-job-token")

	require.NoError(t, runProvenanceRoot(t, "--checksums-file", checksumFile, "--output", "statement.json", "--key", "env://PROVENANCE_KEY"))
}
