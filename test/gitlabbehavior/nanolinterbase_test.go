// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// GitLab checks a merge request out shallow and without its target branch, so
// nanolinter's secret scan and commit checks had nothing to compare with. The
// component fetches the target as origin/<target> with the history that meets
// the merge request. Run the real script against a local server repository
// with GitLab's shallow checkout and a stand-in nanolinter that asks git what
// the real one would.
func TestNanolinterFetchesTheMergeRequestTarget(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	server := filepath.Join(dir, "group", "app.git")
	seed := filepath.Join(dir, "seed")
	workspace := filepath.Join(dir, "builds")

	gitIn(t, dir, "init", "--quiet", "--bare", "--initial-branch=main", server)
	gitIn(t, dir, "init", "--quiet", "--initial-branch=main", seed)

	for _, message := range []string{"one", "two", "three"} {
		gitIn(t, seed, "commit", "--quiet", "--allow-empty", "-m", message)
	}

	gitIn(t, seed, "checkout", "--quiet", "-b", "feature")

	for _, message := range []string{"four", "five"} {
		gitIn(t, seed, "commit", "--quiet", "--allow-empty", "-m", message)
	}

	gitIn(t, seed, "push", "--quiet", server, "main", "feature")
	gitIn(t, dir, "clone", "--quiet", "--depth=1", "--branch=feature", "file://"+server, workspace)

	stub := "#!/bin/sh\nset -e\ngit rev-parse --verify --quiet refs/remotes/origin/main >/dev/null\n" +
		"test \"$(git rev-list --count origin/main..HEAD)\" = 2\n"
	if err := os.WriteFile(filepath.Join(dir, "nanolinter"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := runNanolinterScript(t, dir, workspace, "CI_MERGE_REQUEST_TARGET_BRANCH_NAME=main")
	if err != nil {
		t.Fatalf("the component did not give nanolinter the target branch: %v\n%s", err, output)
	}

	// A target that cannot be fetched is a pipeline error, not a lint finding.
	output, err = runNanolinterScript(t, dir, workspace, "CI_MERGE_REQUEST_TARGET_BRANCH_NAME=gone")
	if err == nil || !strings.Contains(output, "cannot fetch the merge request's target branch gone") {
		t.Fatalf("an unfetchable target: %v\n%s", err, output)
	}
}

func TestNanolinterUsesUTF8AndPreservesScannerFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	stub := `#!/bin/sh
test "$LANG" = C.UTF-8 && test "$LC_ALL" = C.UTF-8 || exit 97
printf 'UTF-8 scanner environment verified\n'
exit 3
`
	if err := os.WriteFile(filepath.Join(dir, "nanolinter"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := runNanolinterScript(t, dir, dir, "LANG=C", "LC_ALL=C")

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("scanner failure must remain blocking: %v\n%s", err, output)
	}

	if !strings.Contains(output, "UTF-8 scanner environment verified") || !strings.Contains(output, "tool/runtime failure") {
		t.Fatalf("missing environment/remediation evidence: %s", output)
	}
}

func TestNanolinterBootstrapCAStaysInvocationScoped(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "lab-ca.pem")
	if err := os.WriteFile(ca, []byte("inert local transport CA fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const scanner = "#!/bin/sh\n[ \"${CURL_CA_BUNDLE:-}\" = public-ca-canary ] || exit 97\nprintf 'public CA retained\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "nanolinter"), []byte(scanner), 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := runNanolinterScript(t, dir, dir, "CI_SERVER_TLS_CA_FILE="+ca, "CURL_CA_BUNDLE=public-ca-canary")
	if err != nil || !strings.Contains(body, "public CA retained") {
		t.Fatalf("lab CA escaped bootstrap into a public scanner: %v\n%s", err, body)
	}
}

func runNanolinterScript(t *testing.T, dir, workspace string, env ...string) (string, error) {
	t.Helper()
	// This suite checks checkout and environment transport. Policy semantics
	// execute against real compiled engines in commands/policy integration tests.
	const policyDriver = "#!/bin/sh\n[ \"$1\" = policy ] && [ \"$2\" = nanolinter ] || exit 98\nshift 2\nif [ \"$1\" = --sarif-file ]; then shift 2; fi\n[ \"$1\" = -- ] || exit 98\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "forge-ci"), []byte(policyDriver), 0o700); err != nil {
		t.Fatal(err)
	}
	// A separately pinned, inert local engine repository supplies bootstrap.
	// It proves the component resolves its admin engine before the lint call;
	// the transport assertions below still run against the real local git.
	engine := filepath.Join(dir, "engine.git")
	gitIn(t, dir, "init", "--quiet", "--initial-branch=main", engine)
	if err := os.MkdirAll(filepath.Join(engine, ".engine", "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	const resolver = "fetch_forge_ci() { if [ -n \"${CI_SERVER_TLS_CA_FILE:-}\" ]; then test \"${CURL_CA_BUNDLE:-}\" = \"${CI_SERVER_TLS_CA_FILE}\"; fi; }\nverify_forge_ci() { test \"${SIGNER_STRICT:-}\" = 1; }\nrun_forge_ci() { forge-ci \"$@\"; }\n"
	if err := os.WriteFile(filepath.Join(engine, ".engine", "bin", "lib-forge-ci.sh"), []byte(resolver), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, engine, "add", ".engine/bin/lib-forge-ci.sh")
	commit := exec.CommandContext(t.Context(), "git", "-C", engine, "-c", "user.name=test", "-c", "user.email=test@invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "fixture: pinned engine")
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("engine commit: %v %s", err, output)
	}
	read := exec.CommandContext(t.Context(), "git", "-C", engine, "rev-parse", "HEAD")
	pin, err := read.Output()
	if err != nil {
		t.Fatal(err)
	}

	content := reporoot.ReadFile(t, "templates/nanolinter.yml")
	decoder := yaml.NewDecoder(bytes.NewReader(content))

	var header map[string]any
	if err := decoder.Decode(&header); err != nil {
		t.Fatal(err)
	}

	var jobs map[string]struct {
		Script []string `yaml:"script"`
	}
	if err := decoder.Decode(&jobs); err != nil {
		t.Fatal(err)
	}

	script := jobs["$[[ inputs.job-name ]]"].Script
	if len(script) != 1 {
		t.Fatal("nanolinter component must have one script block")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	rendered := strings.ReplaceAll(script[0], "$[[ component.sha ]]", strings.TrimSpace(string(pin)))
	command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-c", rendered)
	command.Dir = workspace

	command.Env = append([]string{
		"PATH=" + dir + ":/usr/bin:/bin", "TMPDIR=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"CI_SERVER_URL=file://" + dir, "CI_PROJECT_PATH=group/app", "CI_JOB_TOKEN=job-token",
		"REQUIRED_LINTS=secrets,sast", "ENABLE_GITLAB_SAST=false",
		"RC_FORGE_CI_REPOSITORY=engine",
		"SARIF_FILE=" + filepath.Join(dir, "out.sarif"), "GL_SAST_FILE=" + filepath.Join(dir, "gl.json"),
	}, env...)

	output, err := command.CombinedOutput()

	return string(output), err
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", append([]string{
		"-c", "user.name=test", "-c", "user.email=test@invalid", "-c", "commit.gpgsign=false", "-c", "protocol.file.allow=always",
	}, args...)...)
	command.Dir = dir

	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
