// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	releasecmd "github.com/diggsweden/forge-ci/internal/cli/commands/release"
	validatecmd "github.com/diggsweden/forge-ci/internal/cli/commands/validate"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedenv"
	"github.com/stretchr/testify/require"
)

// Real command wiring and native Git, but only unsigned fixture tags and a
// local file remote. Signature authorization is a separate workflow command.
func TestValidateTagLateRequestCLI(t *testing.T) {
	isolatedenv.Isolate(t)

	git, err := exec.LookPath("git")
	require.NoError(t, err)

	for _, scenario := range []struct {
		name, format string
		noBump       bool
	}{
		{name: "sha1-prepared-child", format: "sha1"},
		{name: "sha1-no-bump", format: "sha1", noBump: true},
		{name: "sha256-prepared-child", format: "sha256"},
		{name: "sha256-no-bump", format: "sha256", noBump: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)

			run := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), git, append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
				cmd.Dir = dir
				body, err := cmd.CombinedOutput()
				require.NoError(t, err, string(body))

				return strings.TrimSpace(string(body))
			}
			run("init", "-q", "--object-format="+scenario.format, "-b", "main")
			run("config", "user.name", "Inert Fixture")
			run("config", "user.email", "fixture@example.invalid")
			run("config", "commit.gpgsign", "false")
			run("config", "tag.gpgsign", "false")
			run("commit", "--allow-empty", "-qm", "request")
			requestSHA := run("rev-parse", "HEAD")
			run("tag", "-a", "release-request/v1.2.3", "-m", "inert request")

			if !scenario.noBump {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte("# 1.2.3\n"), 0o600))
				run("add", "CHANGELOG.md")
				run("commit", "-qm", "prepared")
			}

			releaseSHA := run("rev-parse", "HEAD")
			run("tag", "-a", "v1.2.3", "-m", "inert final")

			remote := filepath.Join(t.TempDir(), "remote.git")
			run("init", "-q", "--object-format="+scenario.format, "--bare", remote)
			run("remote", "add", "origin", remote)
			run("push", "-q", "origin", "refs/tags/release-request/v1.2.3", "refs/tags/v1.2.3")
			t.Setenv("RELEASE_REQUEST_SHA", requestSHA)
			t.Setenv("RELEASE_SHA", releaseSHA)
			t.Setenv("RELEASE_REQUEST_IS_RELEASE", map[bool]string{false: "false", true: "true"}[scenario.noBump])

			args := []string{"release", "validate-tag", "--tag", "v1.2.3", "--expected-parent", requestSHA}
			if !scenario.noBump {
				args = append(args, "--changelog-only")
			}

			for range 2 { // Exact-P publication retry does not rewrite a tag.
				require.NoError(t, releasecmd.New().Run(t.Context(), args))
			}

			run("--git-dir", remote, "update-ref", "-d", "refs/tags/release-request/v1.2.3")
			require.Error(t, releasecmd.New().Run(t.Context(), args))

			// A missing Q remains optional for unrelated legacy CLI calls. With
			// the remote request deleted, this also proves no request lookup.
			t.Setenv("RELEASE_REQUEST_SHA", "")
			t.Setenv("RELEASE_REQUEST_IS_RELEASE", "false")
			require.NoError(t, releasecmd.New().Run(t.Context(), []string{"release", "validate-tag", "--tag", "v1.2.3"}))

			// Replace the remote annotation at the same Q. A stale local object
			// is refused; after fetching that object, source equality can pass
			// but the actual native signature-policy command must still refuse.
			run("tag", "-a", "replacement", requestSHA, "-m", "unsigned replacement at the same source")
			run("push", "-q", "origin", "refs/tags/replacement:refs/tags/release-request/v1.2.3")
			t.Setenv("RELEASE_REQUEST_SHA", requestSHA)
			t.Setenv("RELEASE_REQUEST_IS_RELEASE", map[bool]string{false: "false", true: "true"}[scenario.noBump])
			require.Error(t, releasecmd.New().Run(t.Context(), args))
			run("fetch", "-q", "--force", "origin", "refs/tags/release-request/v1.2.3:refs/tags/release-request/v1.2.3")
			require.NoError(t, releasecmd.New().Run(t.Context(), args))
			err = validatecmd.New().Run(t.Context(), []string{"validate", "tag", "signature", "--tag", "release-request/v1.2.3", "--require-valid-signature", "--require-allowlisted-signer"})
			require.ErrorContains(t, err, "not signed")
		})
	}
}
