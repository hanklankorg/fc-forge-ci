// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package isolatedenv scrubs the test process's environment so a test
// can't accidentally read the developer's ~/.gitconfig, trigger their
// SSH agent, pick up a stray $GNUPGHOME, inherit the CI runner's
// GITHUB_*/CI_*/REGISTRY_* context (which would steer forge detection,
// output sinks, and auth — and could leak a live token), or emit colour
// codes that break golden-file assertions.
//
// Use Isolate(t) at the top of any test that:
//
//   - shells out to git / gpg / ssh (they all read host env)
//   - reads XDG config dirs ($XDG_CONFIG_HOME, $XDG_DATA_HOME, …)
//   - detects the forge/runner or reads run-artifact/registry env
//   - spawns the built binary with os.Environ() (smoke harness)
//   - compares stdout/stderr byte-for-byte (NO_COLOR / TERM)
//
// A package whose tests all want the scrub applies it once from TestMain
// with IsolateProcess; testutil/isolatedgit.Main does that for the packages
// that build scratch repositories with isolatedgit.NewRepo, so the product's
// own git, which inherits os.Environ(), is isolated as well.
//
// WARNING: Isolate calls t.Setenv repeatedly, which is incompatible
// with t.Parallel() at the same scope. Subtests can still use Parallel
// — Go's test framework serialises Setenv at the outer test.
package isolatedenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// scrubCIEnv clears every CI/forge/credential variable to empty (which
// forge-ci's detection reads as "absent" — it uses os.Getenv()!="" /
// truthy, never presence). Isolate clears them via t.Setenv so the host value
// is restored at test-end. A test that needs one of these set re-sets it
// explicitly *after* Isolate, and that later t.Setenv wins.
//
// Prefixes carry CI/forge context, runner wiring, provider/output overrides,
// run-artifact inputs, or registry credentials; the exact list is standalone
// CI/forge/credential vars with no shared prefix. A test must never inherit
// the developer's — or the CI runner's — real values for these: they steer
// forge detection, output-sink selection, and authentication, and some carry
// live tokens against real endpoints.
func scrubCIEnv(setenv func(key, value string)) {
	prefixes := []string{
		"GITHUB_", "ACTIONS_", "RUNNER_", // GitHub Actions runtime + artifact backend
		"GITLAB_", "CI_", // GitLab CI (CI_JOB_TOKEN, CI_API_V4_URL, …)
		"FORGEJO_", "GITEA_", // Forgejo / Gitea Actions
		"FORGE_CI_", "ARTIFACT_", // forge-ci's own provider/runner/format + artifact verb inputs
		"REGISTRY_", // container/package registry auth
		"NPM_", "SIGN_", "SIGSTORE_", "COSIGN_", "GPG_", "SSH_",
	}

	exact := map[string]struct{}{
		"CI": {}, "CONTINUOUS_INTEGRATION": {}, "BUILD_NUMBER": {},
		"BUILDKITE": {}, "CIRCLECI": {}, "TRAVIS": {}, "DRONE": {}, "APPVEYOR": {},
		"JENKINS_URL": {}, "TEAMCITY_VERSION": {},
		"GH_TOKEN": {}, "GH_HOST": {}, "GH_ENTERPRISE_TOKEN": {}, // gh CLI
		"DOCKER_CONFIG": {}, "DOCKER_AUTH_CONFIG": {}, // shared registry auth file
		"GPG_PRIVATE_KEY": {}, "GPG_PASSPHRASE": {}, // release gpg import
		"COSIGN_PASSWORD": {}, "COSIGN_PRIVATE_KEY": {}, "COSIGN_YES": {}, // signing
	}
	for _, key := range append(runcontext.Token().Keys(), runcontext.ReleaseToken().Keys()...) {
		exact[key] = struct{}{}
	}
	// Orchestrated tag aliases have no CI prefix but also satisfy required CLI
	// flags. A release job's tag must not leak into missing-input fixtures.
	for _, key := range runcontext.Tag().Keys() {
		exact[key] = struct{}{}
	}

	for _, kv := range os.Environ() {
		key, _, found := strings.Cut(kv, "=")
		if !found {
			continue
		}

		if _, ok := exact[key]; ok || hasAnyPrefix(key, prefixes) {
			setenv(key, "")
		}
	}
}

func hasAnyPrefix(key string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	return false
}

// Isolate sets a curated set of environment variables pointing into
// throwaway directories under t.TempDir(). All values are restored at
// test-end by Go's t.Setenv cleanup.
//
// Returns the isolated $HOME path so callers that need to write into
// it (e.g. seed a fake gitconfig) can do so.
func Isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()

	// Git distinguishes unset from empty routing variables. Register
	// restoration with testing before unsetting an ambient Git override.
	unset := func(key string) error {
		t.Setenv(key, "")

		return os.Unsetenv(key)
	}

	if err := isolate(home, t.Setenv, unset); err != nil {
		t.Fatalf("isolatedenv: %v", err)
	}

	return home
}

// IsolateProcess applies the same isolation to the whole test process, for a
// TestMain whose tests all want it: it cannot be undone per test, so tests can
// run in parallel. The isolated home is a fresh private directory; cleanup
// removes it after the tests have run.
func IsolateProcess() (string, func(), error) {
	home, err := os.MkdirTemp("", "isolatedenv-")
	if err != nil {
		return "", nil, err
	}

	cleanup := func() { _ = os.RemoveAll(home) }

	var setErrs []error

	setenv := func(key, value string) {
		if err := os.Setenv(key, value); err != nil {
			setErrs = append(setErrs, err)
		}
	}

	if err := errors.Join(append(setErrs, isolate(home, setenv, os.Unsetenv))...); err != nil {
		cleanup()

		return "", nil, err
	}

	return home, cleanup, nil
}

// isolate points the environment at home through setenv, and removes every
// ambient GIT_* variable through unset.
func isolate(home string, setenv func(key, value string), unset func(key string) error) error {
	// Core HOME isolation. USERPROFILE for Windows-targeting tests.
	setenv("HOME", home)
	setenv("USERPROFILE", home)

	// Go's telemetry uploader can outlive a subprocess and race removal of
	// this throwaway HOME. Disable it before any fixture invokes Go.
	// GOTELEMETRY is read-only; Go's test override must target a path that
	// cannot contain telemetry files (including when tests run as root).
	setenv("TEST_TELEMETRY_DIR", os.DevNull)

	// XDG base-directory spec. Tools that read XDG (e.g. mise, devbase,
	// most modern CLIs) look here first; pointing them at $HOME/.config
	// etc. means they can't reach the developer's real config.
	setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))

	// Git: disable system + global gitconfig reads. Tests that want a
	// gitconfig opt in by writing one and pointing GIT_CONFIG_GLOBAL at
	// it. Author/committer identity defaults provide stable signatures.
	// Git distinguishes unset from empty routing variables: unset every
	// ambient Git override, then pin our own.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			if err := unset(key); err != nil {
				return fmt.Errorf("unset %s: %w", key, err)
			}
		}
	}

	setenv("GIT_CONFIG_NOSYSTEM", "1")
	setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	setenv("GIT_AUTHOR_NAME", "Test Bot")
	setenv("GIT_AUTHOR_EMAIL", "bot@example.invalid")
	setenv("GIT_COMMITTER_NAME", "Test Bot")
	setenv("GIT_COMMITTER_EMAIL", "bot@example.invalid")

	// `git init` copies the template directory's hooks into every new repo,
	// and GIT_TEMPLATE_DIR is a real variable that git honours even with all
	// config reads disabled. Stock git ships only .sample hooks, which never
	// run — but a host that exports this would seed an executable hook into
	// every scratch repo a test builds. Point it at an empty directory.
	emptyTemplate := filepath.Join(home, ".git-template")
	if err := os.MkdirAll(emptyTemplate, 0o700); err != nil {
		return fmt.Errorf("mkdir GIT_TEMPLATE_DIR %q: %w", emptyTemplate, err)
	}

	setenv("GIT_TEMPLATE_DIR", emptyTemplate)

	// Interactive prompts — disable all. A test should never hang waiting
	// for password input or page in less/more, and the editor must never
	// fire.
	//
	// Not here, deliberately, because git does not read them: there is no
	// GIT_HOOKS_PATH and no GIT_CREDENTIAL_HELPER. Both were set here once
	// and did nothing. What actually closes each vector:
	//
	//   - hooks: the git adapter passes `-c core.hooksPath=/dev/null` on
	//     every invocation that could fire one, which adapters/git's tests
	//     assert. Setting core.hooksPath here instead would need
	//     GIT_CONFIG_COUNT, which adapters/git/checkout.go already uses for
	//     the auth header — a second writer would silently drop one of them.
	//   - credential helpers: GIT_CONFIG_NOSYSTEM and GIT_CONFIG_GLOBAL
	//     above leave no config for a helper to be declared in.
	//   - template hooks: GIT_TEMPLATE_DIR below.
	setenv("GIT_TERMINAL_PROMPT", "0")
	setenv("GIT_ASKPASS", "")
	setenv("GIT_SSH_COMMAND", "false")
	setenv("GIT_PAGER", "cat")
	setenv("GIT_EDITOR", "false")
	setenv("EDITOR", "false")
	setenv("VISUAL", "false")
	setenv("PAGER", "cat")

	// GPG: own GNUPGHOME under the isolated tree so signing/verifying
	// tests don't touch the developer's keyring. The directory is
	// created on first use by gpg itself.
	setenv("GNUPGHOME", GNUPGHome(home))
	setenv("GPG_TTY", "")

	// SSH agent isolation — empty values mean "no agent available", so
	// SSH operations fall back to keys-on-disk (and fail loudly if
	// they're missing) instead of silently using the developer's agent.
	setenv("SSH_AUTH_SOCK", "")
	setenv("SSH_AGENT_PID", "")
	setenv("SSH_ASKPASS", "")

	// Proxy isolation: prevent the developer's corp proxy / VPN config
	// from routing test traffic. Lowercase + uppercase forms are both
	// honoured by curl, go's net/http, and most CLIs.
	setenv("HTTP_PROXY", "")
	setenv("HTTPS_PROXY", "")
	setenv("ALL_PROXY", "")
	setenv("NO_PROXY", "")
	setenv("http_proxy", "")
	setenv("https_proxy", "")
	setenv("all_proxy", "")
	setenv("no_proxy", "")

	// Temp: pin to the isolated home so subprocess temp files don't
	// leak into the developer's /tmp (and races with parallel runs
	// can't collide on shared paths). Subprocesses (gpg, git) expect
	// the path to already exist, so create it eagerly.
	//
	// All three names, not just TMPDIR. TMPDIR is the Unix one and the
	// only one Go's os.TempDir reads there, but Windows ignores it
	// entirely in favour of TMP and TEMP — and this function already
	// pins USERPROFILE for Windows-targeting tests, so isolating the
	// home while leaving the temp path on the host is half a job.
	// Node's os.tmpdir and Python's tempfile also fall back to TMP and
	// TEMP, so a subprocess that clears TMPDIR still lands here.
	tmp := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return fmt.Errorf("mkdir TMPDIR %q: %w", tmp, err)
	}

	setenv("TMPDIR", tmp)
	setenv("TMP", tmp)
	setenv("TEMP", tmp)

	// Output stability: disable colour, force a terminal type that no
	// CLI tries to be clever about. Golden-file comparisons need bytes
	// that don't depend on the developer's TERM.
	setenv("NO_COLOR", "1")
	setenv("TERM", "dumb")
	setenv("CLICOLOR", "0")

	// Locale: force C locale so date / number formatting is stable
	// across the developer's LC_ALL settings.
	setenv("LC_ALL", "C")
	setenv("LANG", "C")

	// CI/forge/credential isolation: a test (and any binary it spawns with
	// os.Environ()) must not inherit ambient GITHUB_*/ACTIONS_*/CI_*/
	// REGISTRY_*/FORGE_CI_* etc. from the developer's shell or the CI
	// runner. Otherwise forge detection, sinks, and auth would depend on
	// where the suite happens to run, and real tokens could leak in.
	scrubCIEnv(setenv)

	return nil
}

// GNUPGHome is the GnuPG home of the isolated environment rooted at home.
//
// Not named ".gnupg": GnuPG treats $HOME/.gnupg as the default home, and the
// default home uses the unhashed per-user socket directory
// (/run/user/<uid>/gnupg), which is the developer's own gpg-agent. Any other
// name gets a hashed socket directory of its own.
func GNUPGHome(home string) string {
	return filepath.Join(home, "gnupg-home")
}
