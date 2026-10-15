// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The login step hands the token to the engine only on stdin: it is absent
// from argv, from the engine environment and from the shell the resolver is
// sourced into, and the inputs map to their own engine flags. Auth-file
// writing is covered by the forge-ci companion suite.
func TestRegistryLogin(t *testing.T) {
	t.Parallel()
	script := actionStep(t, "registry-login", "login")

	const secret = "registry-token-value-DO-NOT-LOG"

	box := newSandbox(t, "registry-login")
	authFile, envFile := filepath.Join(box.root, "registry-auth.json"), filepath.Join(box.root, "forgejo-env")
	env := append(installRecordingFakes(box),
		"REGISTRY_LOGIN_USERNAME=fixture-user", "REGISTRY_LOGIN_TOKEN="+secret,
		"REGISTRY_LOGIN_REGISTRY=registry.invalid", "REGISTRY_LOGIN_TOOL=buildah",
		"REGISTRY_LOGIN_AUTHFILE="+authFile, "REGISTRY_LOGIN_CREATE_AUTHFILE=true",
		"REGISTRY_LOGIN_EXPORT_ENV=true", "REGISTRY_LOGIN_SERVER=https://forgejo.invalid",
		"FORGEJO_ENV="+envFile,
	)

	got := box.run(script, env...)
	if got.status != 0 || got.stdout+got.stderr != recordingEngineMarker {
		t.Fatalf("status %d stdout %q stderr %q", got.status, got.stdout, got.stderr)
	}

	wantArgv := []string{
		"container", "login", "--registry", "registry.invalid", "--server-url", "https://forgejo.invalid",
		"--registry-username", "fixture-user", "--registry-password-file", "-", "--auth-file", authFile,
		"--create-auth-file=true", "--export-env=true", "--env-file", envFile,
	}
	if !slices.Equal(box.argv(), wantArgv) {
		t.Fatalf("argv = %q", box.argv())
	}

	recorded := recorded(box)
	if recorded.stdin != secret {
		t.Errorf("engine stdin = %q, want the token", recorded.stdin)
	}

	if slices.Contains(recorded.shellVars, "REGISTRY_LOGIN_TOKEN") {
		t.Error("REGISTRY_LOGIN_TOKEN reached the resolver")
	}

	for _, entry := range recorded.envLines {
		if strings.Contains(entry, secret) || strings.Contains(entry, "REGISTRY_LOGIN_TOKEN") {
			t.Errorf("engine environment exposes the token: %q", entry)
		}
	}

	if recorded.env["FORGE_CI_PROVIDER"] != "forgejo" {
		t.Errorf("engine FORGE_CI_PROVIDER = %q", recorded.env["FORGE_CI_PROVIDER"])
	}
}

// A caller that gives only the credential gets a credentials file of its own
// job: under the runner's temporary directory, readable by its owner alone.
// The shared file in HOME, which every later step and, on a runner that keeps
// its home, every later job can read, is written only when asked for.
func TestRegistryLoginDefaultsToAnIsolatedAuthFile(t *testing.T) {
	t.Parallel()

	action := loadAction(t, "registry-login")
	script := action.script(t, "login")

	for _, test := range []struct {
		name, explicit string
	}{
		{name: "no auth file named"},
		{name: "a named auth file wins", explicit: "named-auth.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box, env := engineSandbox(t, "registry-login", nil)
			runnerTemp := filepath.Join(box.root, "runner-temp")

			if err := os.Mkdir(runnerTemp, 0o700); err != nil {
				t.Fatal(err)
			}

			explicit := test.explicit
			if explicit != "" {
				explicit = filepath.Join(box.root, explicit)
			}

			got := box.run(script, append(env, "GITHUB_ACTIONS=true",
				"REGISTRY_LOGIN_USERNAME=fixture-user", "REGISTRY_LOGIN_TOKEN=fixture-token",
				"REGISTRY_LOGIN_REGISTRY=registry.invalid", "REGISTRY_LOGIN_SERVER=https://forgejo.invalid",
				"REGISTRY_LOGIN_TOOL="+fmt.Sprint(action.Inputs["tool"].Default),
				"REGISTRY_LOGIN_AUTHFILE="+explicit,
				"REGISTRY_LOGIN_CREATE_AUTHFILE="+fmt.Sprint(action.Inputs["create-auth-file"].Default),
				"REGISTRY_LOGIN_EXPORT_ENV="+fmt.Sprint(action.Inputs["export-env"].Default),
				"RUNNER_TEMP="+runnerTemp, "FORGEJO_ENV="+filepath.Join(box.root, "forgejo-env"),
			)...)
			if got.status != 0 {
				t.Fatalf("status %d stderr %s", got.status, got.stderr)
			}

			authFile, found := strings.CutPrefix(strings.TrimSpace(box.output()), "auth-file=")
			if !found || authFile == "" {
				t.Fatalf("the step published no auth file: %q; stderr %s", box.output(), got.stderr)
			}

			if explicit != "" && authFile != explicit {
				t.Fatalf("auth file = %s, want the named %s", authFile, explicit)
			}

			if explicit == "" && filepath.Dir(authFile) != runnerTemp {
				t.Fatalf("auth file %s is not in the runner's temporary directory %s", authFile, runnerTemp)
			}

			info, err := os.Stat(authFile)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("auth file %s: mode %v, err %v; want 0600", authFile, info, err)
			}

			if entries, err := os.ReadDir(filepath.Join(box.root, "home")); err != nil || len(entries) != 0 {
				t.Fatalf("the login wrote under HOME: %v, %v", entries, err)
			}
		})
	}
}
