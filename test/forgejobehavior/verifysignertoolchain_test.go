// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const signerToolchainScript = ".forgejo/scripts/verify-signer-toolchain.sh"

// signerToolchainSandbox lays the toolchain check out as a helper checkout
// has it, beside a fixture engine and its recorded checksum, with a PATH that
// holds exactly the tools the check requires: the ones the script itself runs
// are the host's, the rest answer any call with success.
func signerToolchainSandbox(t *testing.T) (*sandbox, string) {
	t.Helper()

	box := newSandbox(t, "unused")
	box.write(signerToolchainScript, string(reporoot.ReadFile(t, signerToolchainScript)), 0o700)

	const engineBody = "#!/usr/bin/env bash\nexit 0\n"

	box.write(".forgejo/bin/forge-ci", engineBody, 0o700)

	sum := sha256.Sum256([]byte(engineBody))
	box.write(trackedChecksum(runtime.GOARCH), hex.EncodeToString(sum[:])+"  bin/forge-ci\n", 0o600)

	tools := filepath.Join(box.root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, tool := range []string{"bash", "awk", "sed", "dirname", "sha256sum", "env", "uname"} {
		host, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("the host has no %s: %v", tool, err)
		}

		if err := os.Symlink(host, filepath.Join(tools, tool)); err != nil {
			t.Fatal(err)
		}
	}

	for _, tool := range []string{"git", "curl", "jq", "gpg", "cosign", "skopeo", "syft", "tar", "mktemp", "find", "grep", "tr", "cut", "sort", "wc"} {
		box.write("tools/"+tool, "#!/bin/sh\nexit 0\n", 0o700)
	}

	return box, tools
}

// The strict toolchain check finds the engine of the checkout it runs from at
// .forgejo/bin, one directory beside its own, and holds it to the checksum
// recorded there. Every privileged job runs this before it touches a secret,
// so an engine that drifted from its pin, or lost it, stops the job here.
//
// The checkout tracks the checksum and the job downloads the engine in the
// step before this one. A checkout without the engine is therefore a job that
// skipped the step, and is refused: its first engine call would otherwise
// have to download in a step that holds a key. The signer image build runs
// the same script with no checkout beside it, neither engine nor checksum,
// and passes.
func TestVerifySignerToolchainHoldsTheLayerEngineToItsPin(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		spoil func(box *sandbox)
		want  int
		say   string
	}{
		{name: "accepts the engine at its recorded checksum", want: 0, say: "forge-ci binary matches the recorded pin"},
		{
			name: "refuses an engine that differs from its checksum", want: 1, say: "does not match the recorded pin",
			spoil: func(box *sandbox) { box.write(".forgejo/bin/forge-ci", "#!/usr/bin/env bash\nexit 1\n", 0o700) },
		},
		{
			name: "refuses an engine without a checksum", want: 1, say: "the recorded pin is missing",
			spoil: func(box *sandbox) {
				if err := os.Remove(filepath.Join(box.root, filepath.FromSlash(trackedChecksum(runtime.GOARCH)))); err != nil {
					box.t.Fatal(err)
				}
			},
		},
		{
			name: "refuses a checkout whose engine was not fetched", want: 1, say: "the layer's forge-ci binary is missing",
			spoil: func(box *sandbox) {
				if err := os.Remove(filepath.Join(box.root, ".forgejo", "bin", "forge-ci")); err != nil {
					box.t.Fatal(err)
				}
			},
		},
		{
			name: "accepts the image build, which has no checkout", want: 0,
			spoil: func(box *sandbox) {
				for _, name := range []string{".forgejo/bin/forge-ci", trackedChecksum(runtime.GOARCH)} {
					if err := os.Remove(filepath.Join(box.root, filepath.FromSlash(name))); err != nil {
						box.t.Fatal(err)
					}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			box, tools := signerToolchainSandbox(t)
			if test.spoil != nil {
				test.spoil(box)
			}

			got := box.run(`bash "${CHECK:?}"`,
				"PATH="+tools, "SIGNER_STRICT=1", "CHECK="+filepath.Join(box.root, filepath.FromSlash(signerToolchainScript)))
			if got.status != test.want || !strings.Contains(got.stdout+got.stderr, test.say) {
				t.Fatalf("status %d, want %d with %q\nstdout %s\nstderr %s", got.status, test.want, test.say, got.stdout, got.stderr)
			}

			if test.want == 0 && test.say != "" && !strings.Contains(got.stdout, box.root+"/.forgejo/scripts/../bin/forge-ci") {
				t.Fatalf("the check did not judge the layer's own engine:\n%s", got.stdout)
			}
		})
	}
}
