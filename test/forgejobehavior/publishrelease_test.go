// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recordingResolver stands in for bin/lib-forge-ci.sh and records every
// shell variable name (exported or not) visible when the action sources it.
const recordingResolver = `compgen -v >"${FAKE_SHELL_VARS_LOG:?}"
run_forge_ci() {
  "$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/forge-ci" "$@"
}
`

// recordingEngine records argv like fakeEngine (so sandbox.argv works), its
// complete environment and its stdin, then prints a fixed marker.
const recordingEngine = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\0' "$@" >"${FAKE_ARGV_LOG:?}"
env -0 >"${FAKE_ENV_LOG:?}"
cat >"${FAKE_STDIN_LOG:?}"
echo 'fake engine ran'
exit "${FAKE_ENGINE_STATUS:-0}"
`

const recordingEngineMarker = "fake engine ran\n"

// recording holds what the recording fakes saw in one run.
type recording struct {
	shellVars []string          // variable names visible to the sourced resolver
	env       map[string]string // engine environment
	envLines  []string          // engine environment, NAME=value entries
	stdin     string            // engine stdin
}

// installRecordingFakes replaces the sandbox resolver and engine with the
// recording fakes and returns the environment entries they need.
func installRecordingFakes(box *sandbox) []string {
	box.t.Helper()
	box.write(".forgejo/bin/lib-forge-ci.sh", recordingResolver, 0o600)
	box.write(".forgejo/bin/forge-ci", recordingEngine, 0o700)

	return []string{
		"FAKE_SHELL_VARS_LOG=" + filepath.Join(box.root, "shell-vars"),
		"FAKE_ENV_LOG=" + filepath.Join(box.root, "engine-env"),
		"FAKE_STDIN_LOG=" + filepath.Join(box.root, "engine-stdin"),
	}
}

// recorded reads what the recording fakes logged in the last run.
func recorded(box *sandbox) recording {
	box.t.Helper()

	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(box.root, name))
		if err != nil {
			box.t.Fatalf("recording fake did not log %s: %v", name, err)
		}

		return string(data)
	}

	recorded := recording{shellVars: strings.Fields(read("shell-vars")), env: map[string]string{}, stdin: read("engine-stdin")}
	for entry := range strings.SplitSeq(strings.TrimSuffix(read("engine-env"), "\x00"), "\x00") {
		recorded.envLines = append(recorded.envLines, entry)
		name, value, _ := strings.Cut(entry, "=")
		recorded.env[name] = value
	}

	return recorded
}

// publishReleaseForbidden are the credential aliases the publish step must
// drop before sourcing the resolver; the token reaches the engine only as the
// FORGEJO_TOKEN it sets on the call itself.
func publishReleaseForbidden() []string {
	return []string{
		"COSIGN_KEY", "COSIGN_PASSWORD", "FORGEJO_TOKEN", "GITEA_TOKEN", "GITHUB_TOKEN",
		"GPG_SIGNING_FINGERPRINT", "GPG_SIGNING_KEY", "GPG_SIGNING_PASSWORD",
		"MISE_FORGEJO_TOKEN", "MISE_GITHUB_TOKEN", "PUBLISH_RELEASE_TOKEN_INPUT",
		"REGISTRY_TOKEN", "REGISTRY_USER", "RELEASE_TOKEN",
	}
}

// The sign-and-publish publish step hands the token to the engine only as
// FORGEJO_TOKEN, drops every ambient credential alias before the resolver and
// engine see them, and maps its inputs to the engine flags and provider
// environment. Release publication itself is covered by the forge-ci
// companion suite.
func TestPublishRelease(t *testing.T) {
	t.Parallel()

	publish := workflowStep(t, "sign-and-publish-release.yml", "publish", "Publish release assets and signatures")
	//nolint:gosec // G101: the name of a secret in a workflow expression, no value.
	if want := map[string]string{
		"PUBLISH_RELEASE_TOKEN_INPUT": "${{ secrets.RELEASE_TOKEN }}",
		"RELEASE_DRAFT":               "${{ inputs['release-draft'] }}",
	}; !maps.Equal(publish.Env, want) {
		t.Fatalf("publish step env = %q, want %q", publish.Env, want)
	}

	script := publish.Run

	const secret = "publish-token-value-DO-NOT-LOG"

	box := newSandbox(t, "sign-and-publish")

	env := installRecordingFakes(box)
	for _, name := range publishReleaseForbidden() {
		env = append(env, name+"=forbidden-"+strings.ToLower(name))
	}

	env = append(env,
		"PUBLISH_RELEASE_TOKEN_INPUT="+secret, "RELEASE_DRAFT=false",
		"HELPERS_DIR="+box.root, "CONSUMER_WORKSPACE="+t.TempDir(),
		"FORGEJO_SERVER=https://forgejo.invalid", "FORGEJO_REPO=fixture-user/example",
		"RELEASE_TAG=v0.0.1", "DIST_DIR=dist", "RELEASE_FILES_MANIFEST=dist/release-files.json",
	)

	got := box.run(script, env...)
	if got.status != 0 || got.stdout+got.stderr != recordingEngineMarker {
		t.Fatalf("status %d stdout %q stderr %q", got.status, got.stdout, got.stderr)
	}

	wantArgv := []string{
		"release", "publish", "--tag", "v0.0.1", "--repository", "fixture-user/example",
		"--release-name-from-repository", "--release-notes-file", "dist/release-notes.md",
		"--draft=false", "--manifest", "dist/release-files.json", "--dist-dir", "dist",
	}
	if !slices.Equal(box.argv(), wantArgv) {
		t.Fatalf("argv = %q", box.argv())
	}

	recorded := recorded(box)
	for _, name := range publishReleaseForbidden() {
		if slices.Contains(recorded.shellVars, name) {
			t.Errorf("%s reached the resolver", name)
		}

		if _, ok := recorded.env[name]; ok && name != "FORGEJO_TOKEN" {
			t.Errorf("%s reached the engine", name)
		}
	}

	var carriers []string

	for _, entry := range recorded.envLines {
		if strings.Contains(entry, secret) {
			carriers = append(carriers, entry)
		}
	}

	if !slices.Equal(carriers, []string{"FORGEJO_TOKEN=" + secret}) {
		t.Errorf("token carried by %q, want only FORGEJO_TOKEN", carriers)
	}

	for name, want := range map[string]string{
		"FORGEJO_SERVER_URL": "https://forgejo.invalid", "FORGEJO_REPOSITORY": "fixture-user/example",
		"FORGE_CI_PROVIDER": "forgejo",
	} {
		if recorded.env[name] != want {
			t.Errorf("engine %s = %q, want %q", name, recorded.env[name], want)
		}
	}
}
