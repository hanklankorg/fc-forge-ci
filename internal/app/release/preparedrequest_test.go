// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitadapter "github.com/diggsweden/forge-ci/internal/adapters/git"
	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedenv"
	"github.com/stretchr/testify/require"
)

func TestPreparedRequestBindingUsesExactRemoteAndCallerCredential(t *testing.T) {
	t.Parallel()

	parent, requestObject := strings.Repeat("b", 40), strings.Repeat("c", 40)

	for _, scenario := range []string{"valid", "moved-object", "moved-commit", "object-lookup-failure", "commit-lookup-failure"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			var calls []tagVerifyCall

			credential := runcontext.ReleaseToken().Resolve(func(string) string { return "inert-read-token" })
			git := fakeTagGit{head: sha, tagCommit: sha, parents: []string{parent}, request: parent, requestObject: requestObject, changed: []string{"CHANGELOG.md"}}

			git.observe = func(_ context.Context, call tagVerifyCall) error {
				calls = append(calls, call)
				if call.ref == "release-request/v1.2.3" {
					require.Equal(t, "original-remote", call.repository)
					require.Equal(t, credential, call.token)

					if scenario == "object-lookup-failure" && call.method == "RemoteTagObjectAtURL" || scenario == "commit-lookup-failure" && call.method == "RemoteTagCommit" {
						return errVerifyTagRemoteCommit
					}
				}

				return nil
			}
			if scenario == "moved-object" {
				git.remoteRequestObject = strings.Repeat("d", 40)
			}

			if scenario == "moved-commit" {
				git.remoteRequestCommit = sha
			}

			var out bytes.Buffer

			err := apprelease.VerifyReleaseTag(t.Context(), git, &out, apprelease.VerifyReleaseTagInput{
				ReleaseSHA: sha, Tag: "v1.2.3", RepoURL: "original-remote", Token: credential, ExpectedParent: parent, ChangelogOnly: true,
			})
			if scenario == "valid" {
				require.NoError(t, err)
				require.Contains(t, calls, tagVerifyCall{method: "RemoteTagCommit", ref: "release-request/v1.2.3", repository: "original-remote", token: credential})
			} else {
				require.Error(t, err)
				require.Empty(t, out.String())
				require.NotEqual(t, "RemoteVersionTags", calls[len(calls)-1].method, "stop at the request boundary")

				if strings.HasSuffix(scenario, "lookup-failure") {
					require.ErrorIs(t, err, errVerifyTagRemoteCommit)
				}
			}
		})
	}
}

// Native Git fixtures use only local bare remotes, unsigned annotated tags and
// isolated identity/config. They prove reference/object semantics, not signatures.
func TestPreparedRequestBindingWithNativeGit(t *testing.T) {
	isolatedenv.Isolate(t)

	gitBin, err := exec.LookPath("git")
	require.NoError(t, err)

	for _, scenario := range []string{"valid", "lightweight-request", "cargo-root", "moved-remote-local-stale", "replaced-object-same-commit", "missing-request", "wrong-parent", "merge", "source-change", "cargo-nested", "cargo-not-version-only"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			run := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), gitBin, append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
				cmd.Dir = dir
				body, err := cmd.CombinedOutput()
				require.NoError(t, err, string(body))

				return strings.TrimSpace(string(body))
			}
			write := func(path, body string) {
				t.Helper()
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte(body), 0o600))
			}

			run("init", "-q", "-b", "main")
			run("config", "user.name", "Inert Fixture")
			run("config", "user.email", "fixture@example.invalid")
			run("config", "commit.gpgsign", "false")
			run("config", "tag.gpgsign", "false")

			cargoPath := "Cargo.toml"
			if scenario == "cargo-nested" {
				cargoPath = "nested/Cargo.toml"
			}

			write(cargoPath, "[package]\nname = \"fixture\"\nversion = \"1.2.2\"\n")
			run("add", ".")
			run("commit", "-q", "-m", "request source")
			requestSHA := run("rev-parse", "HEAD")

			requestTag := "release-request/v1.2.3"
			if scenario == "lightweight-request" {
				run("tag", requestTag)
			} else {
				run("tag", "-a", requestTag, "-m", "inert request annotation")
			}

			if scenario == "wrong-parent" {
				run("commit", "--allow-empty", "-q", "-m", "unauthorised intermediate")
			}

			write("CHANGELOG.md", "# 1.2.3\n")

			if strings.HasPrefix(scenario, "cargo-") {
				body := "[package]\nname = \"fixture\"\nversion = \"1.2.3\"\n"
				if scenario == "cargo-not-version-only" {
					body += "build = \"unapproved.rs\"\n"
				}

				write(cargoPath, body)
			}

			if scenario == "source-change" {
				write("main.go", "package main\n")
			}

			run("add", ".")
			run("commit", "-q", "-m", "prepared child")

			if scenario == "merge" {
				other := run("commit-tree", "HEAD^{tree}", "-p", requestSHA, "-m", "other")
				merge := run("commit-tree", "HEAD^{tree}", "-p", requestSHA, "-p", other, "-m", "merge")
				run("update-ref", "HEAD", merge)
			}

			preparedSHA := run("rev-parse", "HEAD")
			require.NotEqual(t, requestSHA, preparedSHA)
			run("tag", "-a", "v1.2.3", "-m", "inert final annotation")

			remote := filepath.Join(t.TempDir(), "remote.git")
			run("init", "-q", "--bare", remote)
			run("remote", "add", "origin", remote)
			run("push", "-q", "origin", "HEAD:refs/heads/main", "refs/tags/"+requestTag, "refs/tags/v1.2.3")

			switch scenario {
			case "moved-remote-local-stale":
				run("--git-dir", remote, "update-ref", "refs/tags/"+requestTag, preparedSHA)
			case "replaced-object-same-commit":
				run("tag", "-a", "replacement", requestSHA, "-m", "different request object")
				run("push", "-q", "origin", "refs/tags/replacement")
				run("--git-dir", remote, "update-ref", "refs/tags/"+requestTag, run("rev-parse", "refs/tags/replacement"))
			case "missing-request":
				run("--git-dir", remote, "update-ref", "-d", "refs/tags/"+requestTag)
			}

			in := apprelease.VerifyReleaseTagInput{ReleaseSHA: preparedSHA, Tag: "v1.2.3", ExpectedParent: requestSHA, ChangelogOnly: true}
			if strings.HasPrefix(scenario, "cargo-") {
				in.PreparePlan = bumpPlanOf(projecttype.Cargo)
			}

			var out bytes.Buffer

			err := apprelease.VerifyReleaseTag(t.Context(), &gitadapter.Repo{Dir: dir}, &out, in)
			if scenario == "valid" || scenario == "lightweight-request" || scenario == "cargo-root" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errs.ErrValidation)
				require.Empty(t, out.String())

				if scenario == "moved-remote-local-stale" || scenario == "replaced-object-same-commit" || scenario == "missing-request" {
					require.ErrorContains(t, err, "request object")
				}
			}
		})
	}
}

func TestPreparedRequestBindingRejectsRequestAsPreparedCommit(t *testing.T) {
	t.Parallel()

	queries := 0
	err := apprelease.VerifyReleaseTag(t.Context(), fakeTagGit{queries: &queries}, io.Discard,
		apprelease.VerifyReleaseTagInput{ReleaseSHA: sha, Tag: "v1.2.3", ExpectedParent: sha})
	require.ErrorIs(t, err, errs.ErrValidation)
	require.Zero(t, queries)
}
