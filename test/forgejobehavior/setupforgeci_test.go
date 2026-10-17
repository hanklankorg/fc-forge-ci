// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// setupForgeCISandbox lays out the real setup script and resolver next to a
// fixture bin/forge-ci running body, recorded in the checksum file of the
// host's architecture.
// Every fixture start appends to the returned log, so a refusal can prove the
// binary never ran.
func setupForgeCISandbox(t *testing.T, body string) (*sandbox, string, string) {
	t.Helper()
	box := newSandbox(t, "setup-forge-ci")
	box.write(".forgejo/bin/lib-forge-ci.sh", string(reporoot.ReadFile(t, ".forgejo/bin/lib-forge-ci.sh")), 0o600)
	box.write(".forgejo/actions/setup-forge-ci/setup-forge-ci.sh", string(reporoot.ReadFile(t, ".forgejo/actions/setup-forge-ci/setup-forge-ci.sh")), 0o700)
	starts := filepath.Join(box.root, "starts")
	fixture := "#!/usr/bin/env bash\nprintf 'started\\n' >>'" + starts + "'\n" + body + "\n"
	box.write(".forgejo/bin/forge-ci", fixture, 0o755)
	sum := sha256.Sum256([]byte(fixture))
	digest := hex.EncodeToString(sum[:])
	box.write(trackedChecksum(runtime.GOARCH), digest+"  bin/forge-ci\n", 0o600)

	return box, digest, starts
}

// The setup step verifies the layer's engine against its recorded checksum
// and an optional caller pin, validates its inputs and version output before
// exposing anything, and then publishes the path, digest and version.
func TestSetupForgeCI(t *testing.T) {
	t.Parallel()
	script := actionStep(t, "setup-forge-ci", "setup")

	const version = "forge-ci fixture 1.2.3"

	printVersion := "printf '%s\\n' '" + version + "'"
	longest := strings.Repeat("v", 256)

	t.Run("refusals", func(t *testing.T) {
		t.Parallel()

		for _, test := range []setupForgeCIRefusal{
			{name: "binary differs from the recorded checksum", body: printVersion, mismatch: true, want: 1, reason: "does not match the recorded pin"},
			{name: "binary differs from expected-sha256", body: printVersion, want: 1, reason: "forge-ci does not match expected-sha256",
				pin: func(string) string { return strings.Repeat("0", 64) }},
			{name: "uppercase pin", body: printVersion, want: 1, reason: "expected-sha256 must be 64 lowercase hexadecimal characters",
				pin: strings.ToUpper},
			{name: "short pin", body: printVersion, want: 1, reason: "expected-sha256 must be 64 lowercase hexadecimal characters",
				pin: func(d string) string { return d[1:] }},
			{name: "long pin", body: printVersion, want: 1, reason: "expected-sha256 must be 64 lowercase hexadecimal characters",
				pin: func(d string) string { return d + "0" }},
			{name: "non-hex pin", body: printVersion, want: 1, reason: "expected-sha256 must be 64 lowercase hexadecimal characters",
				pin: func(d string) string { return d[1:] + "g" }},
			{name: "add-to-path not a boolean", body: printVersion, env: []string{"ADD_TO_PATH=yes"}, want: 1, reason: "add-to-path must be true or false"},
			{name: "version probe fails", body: "exit 42", want: 42, reason: "forge-ci --version failed (exit 42)", started: true},
			{name: "version over 256 characters", body: "printf '%s\\n' '" + longest + "v'", want: 1, reason: "forge-ci returned unsafe or empty version output", started: true},
			{name: "empty version", body: "exit 0", want: 1, reason: "forge-ci returned unsafe or empty version output", started: true},
			{name: "two-line version", body: "printf 'one\\ntwo\\n'", want: 1, reason: "forge-ci returned unsafe or empty version output", started: true},
			{name: "carriage return in version", body: "printf 'fixture\\r\\n'", want: 1, reason: "forge-ci returned unsafe or empty version output", started: true},
		} {
			t.Run(test.name, func(t *testing.T) { testSetupForgeCIRefusal(t, script, test) })
		}
	})

	t.Run("accepts a 256-character version", func(t *testing.T) {
		t.Parallel()

		s, digest, _ := setupForgeCISandbox(t, "printf '%s\\n' '"+longest+"'")

		got := s.run(script, "ADD_TO_PATH=false")
		if got.status != 0 || got.stdout != longest+" sha256:"+digest+"\n" {
			t.Fatalf("status %d stdout %q; stderr %s", got.status, got.stdout, got.stderr)
		}
	})

	for _, test := range []struct {
		name, addToPath string
		github          bool
	}{
		{"exposes the binary on the Forgejo path and outputs", "true", false},
		{"falls back to the GitHub path and outputs", "true", true},
		{"leaves the path alone when add-to-path is false", "false", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			testSetupForgeCIExposure(t, script, printVersion, version, test.addToPath, test.github)
		})
	}
}

// setupForgeCIRefusal is one refused setup-forge-ci run: the fake
// binary body, the pin and environment offered, and the refusal expected.
type setupForgeCIRefusal struct {
	name, body, reason string
	pin                func(digest string) string
	env                []string
	mismatch           bool
	want               int
	started            bool
}

// testSetupForgeCIRefusal checks that the step refuses with the expected
// status and reason, exposes nothing, and starts the binary only when the
// refusal comes from the binary's own output.
func testSetupForgeCIRefusal(t *testing.T, script string, test setupForgeCIRefusal) {
	t.Helper()

	box, digest, starts := setupForgeCISandbox(t, test.body)
	if test.mismatch {
		box.write(trackedChecksum(runtime.GOARCH), strings.Repeat("0", 64)+"  bin/forge-ci\n", 0o600)
	}

	env := []string{"ADD_TO_PATH=false"}
	if test.pin != nil {
		env = append(env, "EXPECTED_SHA256="+test.pin(digest))
	}

	got := box.run(script, append(env, test.env...)...)
	if got.status != test.want || !strings.Contains(got.stderr, test.reason) {
		t.Fatalf("status %d, want %d for %q; stderr %s", got.status, test.want, test.reason, got.stderr)
	}

	if got.stdout != "" || box.output() != "" {
		t.Fatalf("refusal exposed stdout %q outputs %q", got.stdout, box.output())
	}

	if _, err := os.Stat(starts); test.started == errors.Is(err, os.ErrNotExist) {
		t.Fatalf("binary started = %t, want %t", !errors.Is(err, os.ErrNotExist), test.started)
	}
}

// testSetupForgeCIExposure checks the outputs and path file the step
// writes on the Forgejo or GitHub surface.
func testSetupForgeCIExposure(t *testing.T, script, printVersion, version, addToPath string, github bool) {
	t.Helper()

	box, digest, _ := setupForgeCISandbox(t, printVersion)
	pathFile := box.write("path", "", 0o600)
	outputFile := box.outputs

	env := []string{"ADD_TO_PATH=" + addToPath, "EXPECTED_SHA256=" + digest, "FORGEJO_PATH=" + pathFile}

	if github {
		outputFile = box.write("github-output", "", 0o600)
		env = append(env, "FORGEJO_PATH=", "FORGEJO_OUTPUT=", "GITHUB_PATH="+pathFile, "GITHUB_OUTPUT="+outputFile)
	}

	got := box.run(script, env...)
	// The version is the same for every build of the layer, so the step
	// prints the digest beside it.
	if got.status != 0 || got.stdout != version+" sha256:"+digest+"\n" {
		t.Fatalf("status %d stdout %q; stderr %s", got.status, got.stdout, got.stderr)
	}

	outputs, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatal(err)
	}

	want := "binary-path=" + filepath.Join(box.root, ".forgejo", "bin", "forge-ci") + "\nsha256=" + digest + "\nversion=" + version + "\n"
	if string(outputs) != want {
		t.Fatalf("outputs = %q, want %q", outputs, want)
	}

	path, err := os.ReadFile(pathFile)
	if err != nil {
		t.Fatal(err)
	}

	wantPath := ""
	if addToPath == "true" {
		wantPath = filepath.Join(box.root, ".forgejo", "bin") + "\n"
	}

	if string(path) != wantPath {
		t.Fatalf("path file = %q, want %q", path, wantPath)
	}
}
