// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// .forgejo/bin/lib-forge-ci.sh runs a binary only after it matches the one recorded
// checksum line, and in strict signer mode holds an override to the same pin.
// A non-strict override is the development seam and skips the pin. A verified
// binary runs once per call with the caller's argv and status, with no probe
// of its own. The tracked-binary mismatch case is covered by TestSetupForgeCI.
func TestForgeCIResolver(t *testing.T) {
	t.Parallel()

	const (
		body = `printf '%s\n' "$*" >>"${FAKE_ARGV_LOG:?}"
[ "${1:-}" != fail ] || exit 23`
		calls = `source "${GITHUB_ACTION_PATH}/../../bin/lib-forge-ci.sh"
run_forge_ci intended invocation
run_forge_ci fail
`
	)

	for _, test := range []struct {
		name   string
		setup  func(s *sandbox, digest string) []string
		status int
		reason string
		argv   string
	}{
		{
			name: "runs a matching binary with the caller's argv and status",
			setup: func(*sandbox, string) []string {
				return nil
			},
			status: 23, argv: "intended invocation\nfail\n",
		},
		{
			name: "refuses a checksum file with a second line",
			setup: func(s *sandbox, digest string) []string {
				s.write(trackedChecksum(runtime.GOARCH), digest+"  bin/forge-ci\n"+strings.Repeat("0", 64)+"  bin/forge-ci\n", 0o600)

				return nil
			},
			status: 69, reason: "checksum is missing or malformed",
		},
		{
			name: "refuses a missing checksum file",
			setup: func(s *sandbox, _ string) []string {
				if err := os.Remove(filepath.Join(s.root, filepath.FromSlash(trackedChecksum(runtime.GOARCH)))); err != nil {
					s.t.Fatal(err)
				}

				return nil
			},
			status: 69, reason: "checksum is missing or malformed",
		},
		{
			name: "refuses an unpinned override in strict signer mode",
			setup: func(s *sandbox, _ string) []string {
				override := s.write("override", "#!/usr/bin/env bash\n"+body+"\n# differs from the pin\n", 0o700)

				return []string{"FORGE_CI_BIN=" + override, "SIGNER_STRICT=1"}
			},
			status: 69, reason: "does not match the recorded pin",
		},
		{
			name: "runs an unpinned override outside strict mode",
			setup: func(s *sandbox, _ string) []string {
				override := s.write("override", "#!/usr/bin/env bash\n"+body+"\n# differs from the pin\n", 0o700)

				return []string{"FORGE_CI_BIN=" + override}
			},
			status: 23, argv: "intended invocation\nfail\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, digest, _ := setupForgeCISandbox(t, body)
			log := filepath.Join(s.root, "calls")
			got := s.run(calls, append(test.setup(s, digest), "FAKE_ARGV_LOG="+log)...)

			argv, err := os.ReadFile(log)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}

			if got.status != test.status || string(argv) != test.argv || !strings.Contains(got.stderr, test.reason) {
				t.Fatalf("status %d argv %q, want %d %q; stderr %s", got.status, argv, test.status, test.argv, got.stderr)
			}
		})
	}
}

// The resolver runs the binary of the host's architecture, as uname -m names
// it on Linux and the BSDs, and refuses a host the layer has no binary for
// rather than run one under emulation.
func TestForgeCIResolverSelectsTheHostArchitecture(t *testing.T) {
	t.Parallel()

	const body = `printf 'ran %s\n' "$*"`

	for machine, arch := range map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64", "riscv64": ""} {
		t.Run(machine, func(t *testing.T) {
			t.Parallel()

			s, digest, _ := setupForgeCISandbox(t, body)
			if err := os.Remove(filepath.Join(s.root, filepath.FromSlash(trackedChecksum(runtime.GOARCH)))); err != nil {
				t.Fatal(err)
			}

			if arch != "" {
				s.write(trackedChecksum(arch), digest+"  bin/forge-ci\n", 0o600)
			}

			s.write("fake-bin/uname", "#!/bin/sh\nprintf '%s\\n' '"+machine+"'\n", 0o700)

			got := s.run(`source "${GITHUB_ACTION_PATH}/../../bin/lib-forge-ci.sh"`+"\nrun_forge_ci sign\n",
				"PATH="+filepath.Join(s.root, "fake-bin")+":"+os.Getenv("PATH"))

			switch {
			case arch == "" && (got.status != 69 || got.stdout != "" || !strings.Contains(got.stderr, "this host is '"+machine+"'; use a native host")):
				t.Fatalf("status %d stdout %q, want a refusal of %s; stderr %s", got.status, got.stdout, machine, got.stderr)
			case arch != "" && (got.status != 0 || got.stdout != "ran sign\n"):
				t.Fatalf("status %d stdout %q, want the %s binary to run; stderr %s", got.status, got.stdout, arch, got.stderr)
			}
		})
	}
}
