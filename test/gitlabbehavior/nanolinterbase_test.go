// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"bytes"
	"cmp"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

// The lint image's user does not own the runner's checkout, so git refuses
// it as dubious ownership. The component trusts the checkout through
// GIT_CONFIG_* for every git call, nanolinter's included, and keeps the
// consumer's own entries.
func TestNanolinterTrustsTheCheckoutItDoesNotOwn(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	workspace := filepath.Join(dir, "builds")
	gitIn(t, dir, "init", "--quiet", "--initial-branch=main", workspace)

	stub := "#!/bin/sh\nset -e\n" +
		"test \"$(git config --get-all safe.directory)\" = \"$(printf '%s\\n%s' /consumer/own \"$CI_PROJECT_DIR\")\"\n" +
		"test \"$(git config --get core.consumer)\" = kept\n"
	if err := os.WriteFile(filepath.Join(dir, "nanolinter"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}

	output, err := runNanolinterScript(t, dir, workspace, "GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=/consumer/own",
		"GIT_CONFIG_KEY_1=core.consumer", "GIT_CONFIG_VALUE_1=kept")
	if err != nil {
		t.Fatalf("nanolinter did not see the checkout trusted beside the consumer's entries: %v\n%s", err, output)
	}

	output, err = runNanolinterScript(t, dir, workspace, "GIT_CONFIG_COUNT=two")
	if err == nil || !strings.Contains(output, "GIT_CONFIG_COUNT is not a number") {
		t.Fatalf("a malformed GIT_CONFIG_COUNT: %v\n%s", err, output)
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

// The administrator's settings reach the engine as its flags, from the inputs
// as written: a project variable named like the shell value the component
// assigns them to changes nothing. The engine itself refuses a variable named
// like a setting (see internal/domain/enforcement).
func TestNanolinterGivesTheEngineTheAdministratorSettings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nanolinter"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, settings := range [][2]string{{"false", "true"}, {"true", "false"}} {
		inputs := map[string]string{"enforce-lint": settings[0], "enforce-vulnerabilities": settings[1]}

		output, err := runNanolinterScriptWith(t, dir, dir, inputs, "RC_ADMIN_LINT=hostile", "RC_ADMIN_VULNERABILITIES=hostile")
		if err != nil {
			t.Fatalf("%v: %v\n%s", inputs, err, output)
		}

		flags, err := os.ReadFile(filepath.Join(dir, "admin-flags"))
		if want := "--enforce-lint=" + settings[0] + "\n--enforce-vulnerabilities=" + settings[1] + "\n"; err != nil || string(flags) != want {
			t.Fatalf("%v: the engine got %q, want %q (%v)", inputs, flags, want, err)
		}
	}
}

// disable-checks reaches nanolinter as --disable-check, beside the required
// floor, and a SAST report that cannot be written fails a job whose gate passed.
func TestNanolinterLeavesOutDisabledChecksAndFailsOnALostSASTReport(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	const stub = "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$ENGINE_DIR/nanolinter-args\"\n"
	if err := os.WriteFile(filepath.Join(dir, "nanolinter"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "out.sarif"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	inputs := map[string]string{"enforce-lint": "true", "enforce-vulnerabilities": "true", "disable-checks": "osv,trivy-fs", "enable-gitlab-sast": "true"}

	output, err := runNanolinterScriptWith(t, dir, dir, inputs)
	if err != nil {
		t.Fatalf("a passing gate with a written report failed: %v\n%s", err, output)
	}

	args, err := os.ReadFile(filepath.Join(dir, "nanolinter-args"))
	if want := "verify --require secrets,sast --disable-check osv,trivy-fs --report sarif --out -\n"; err != nil || string(args) != want {
		t.Fatalf("nanolinter got %q, want %q (%v)", args, want, err)
	}

	output, err = runNanolinterScriptWith(t, dir, dir, inputs, "CONVERT_STATUS=3")
	if err == nil || !strings.Contains(output, "SAST report could not be written") {
		t.Fatalf("a lost SAST report passed: %v\n%s", err, output)
	}
}

func runNanolinterScript(t *testing.T, dir, workspace string, env ...string) (string, error) {
	t.Helper()

	return runNanolinterScriptWith(t, dir, workspace, map[string]string{"enforce-lint": "true", "enforce-vulnerabilities": "true"}, env...)
}

func runNanolinterScriptWith(t *testing.T, dir, workspace string, inputs map[string]string, env ...string) (string, error) {
	t.Helper()
	// This suite checks checkout and environment transport. Policy semantics
	// execute against real compiled engines in commands/policy integration tests.
	// The driver records the administrator flags it was given.
	// It stands in for the SAST conversion too, with the status the test names.
	const policyDriver = "#!/bin/sh\n[ \"$1 $2 $3\" != 'security report to-gitlab-sast' ] || exit \"${CONVERT_STATUS:-0}\"\n" +
		"[ \"$1\" = policy ] && [ \"$2\" = nanolinter ] || exit 98\nshift 2\n: > \"${0%/*}/admin-flags\"\n" +
		"while :; do\n  case \"$1\" in\n    --enforce-*=*) printf '%s\\n' \"$1\" >> \"${0%/*}/admin-flags\"; shift ;;\n" +
		"    --sarif-file) shift 2 ;;\n    *) break ;;\n  esac\ndone\n[ \"$1\" = -- ] || exit 98\nshift\nexec \"$@\"\n"
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

	ctx := commandContext(t)

	// The local forge stands in for the instance, under the name the job
	// holds CI_SERVER_URL to.
	rendered := strings.NewReplacer("$[[ component.sha ]]", strings.TrimSpace(string(pin)), "$[[ inputs.server-url ]]", "file://"+dir,
		"$[[ inputs.required-lints ]]", "secrets,sast", "$[[ inputs.disable-checks ]]", inputs["disable-checks"],
		"$[[ inputs.enable-gitlab-sast ]]", cmp.Or(inputs["enable-gitlab-sast"], "false"),
		"$[[ inputs.enforce-lint ]]", inputs["enforce-lint"], "$[[ inputs.enforce-vulnerabilities ]]", inputs["enforce-vulnerabilities"]).Replace(script[0])
	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c", rendered)
	command.Dir = workspace

	command.Env = append([]string{
		"PATH=" + dir + ":/usr/bin:/bin", "TMPDIR=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"CI_SERVER_URL=file://" + dir, "CI_PROJECT_PATH=group/app", "CI_PROJECT_DIR=" + workspace, "CI_JOB_TOKEN=job-token",
		"RC_FORGE_CI_REPOSITORY=engine", "ENGINE_DIR=" + dir,
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
