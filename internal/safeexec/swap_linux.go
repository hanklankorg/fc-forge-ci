// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package safeexec

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cliio"
	"github.com/diggsweden/forge-ci/internal/docslink"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// procSwapsPath is the kernel-published list of active swap areas. Unit tests
// in this package point it at a fixture; nothing else changes it.
var procSwapsPath = "/proc/swaps"

// procSelfCgroupPath names this process's cgroup and cgroupRoot is where the
// cgroup v2 hierarchy is mounted. Unit tests in this package point them at
// fixtures; nothing else changes them.
var (
	procSelfCgroupPath = "/proc/self/cgroup"
	cgroupRoot         = "/sys/fs/cgroup"
)

// procSwapsOverrideEnv names a file read instead of /proc/swaps. It is the
// swap-state injection point of the black-box suite, which drives the real
// binary. It used to be treated as the kernel's answer; a pass read from it is
// now reported as SwapOverridden, so a job that sets it cannot present fixture
// bytes as measured kernel state without the log saying so.
const procSwapsOverrideEnv = "FORGE_CI_PROC_SWAPS"

// SwapEnabled reports whether the running kernel has any active swap
// area, by reading /proc/swaps. /proc/swaps always has a header line;
// any subsequent line is one active swap.
//
// Returns (false, error) when /proc/swaps cannot be read — restricted
// containers, hardened sandboxes, exotic kernels. Callers should soft-
// pass on (false, non-nil err) rather than refuse: claiming swap is
// off requires positive evidence, and the inverse claim requires
// positive evidence too.
func SwapEnabled() (bool, error) {
	path := procSwapsPath
	if override := os.Getenv(procSwapsOverrideEnv); override != "" {
		path = override
	}

	// The override is a file path, never stdin. Share the regular-file and
	// size guards so a FIFO cannot block the check or a huge file exhaust memory.
	body, err := cliio.ReadRegularFile(path)
	if err != nil {
		return false, err
	}

	// /proc/swaps:
	//   Filename Type Size Used Priority
	//   /dev/sda2 partition 2097148 0 -2
	// Header alone → no swap. Two+ lines → swap.
	return bytes.Count(body, []byte{'\n'}) > 1, nil
}

// swapDisabledCgroup returns the cgroup v2 directory whose memory.swap.max is
// 0, checking this process's cgroup and then each ancestor up to cgroupRoot.
// Such a limit binds every process below it, so the kernel will not swap this
// process's memory out even though /proc/swaps, which is host-wide, lists an
// active swap area. Within a cgroup namespace the process's path is "/" and
// cgroupRoot itself is its cgroup.
//
// It reports false when no limit of 0 is found or the files cannot be read:
// cgroup v1, a missing memory controller, an unreadable /proc/self/cgroup,
// or a path outside the cgroup namespace.
// Only positive evidence lets the caller rely on it.
func swapDisabledCgroup() (string, bool) {
	body, err := cliio.ReadRegularFile(procSelfCgroupPath)
	if err != nil {
		return "", false
	}

	// The cgroup v2 entry is the one line "0::<path>"; v1 lines name their
	// controllers between the colons.
	var path string

	found := false

	for line := range strings.Lines(string(body)) {
		if rest, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "0::"); ok {
			path, found = rest, true

			break
		}
	}

	// A path with ".." names a cgroup outside this process's cgroup namespace;
	// below root it would point at an unrelated cgroup, so it is refused.
	if !found || !strings.HasPrefix(path, "/") || path != filepath.Clean(path) || slices.Contains(strings.Split(path, "/"), "..") {
		return "", false
	}

	root := filepath.Clean(cgroupRoot)

	for dir := filepath.Join(root, path); ; dir = filepath.Dir(dir) {
		limit, err := cliio.ReadRegularFile(filepath.Join(dir, "memory.swap.max"))
		if err == nil && string(bytes.TrimSpace(limit)) == "0" {
			return dir, true
		}

		// Stops at root, and at the filesystem root should dir ever leave it.
		if dir == root || dir == filepath.Dir(dir) {
			return "", false
		}
	}
}

// RequireNoSwap is the policy gate for any subcommand that brings
// decrypted private-key material into the Go heap. It refuses to
// proceed when this process's memory can be swapped out, because
// decrypted scalars in the heap could be paged to disk and recovered
// post-job.
//
// Memory can be swapped out when the kernel has an active swap area and
// no cgroup v2 limit forbids this process to use it. /proc/swaps is
// host-wide, so a job container sees the host's swap even when the
// container runs with swap disabled (memory-swap equal to memory, which
// sets memory.swap.max to 0); swapDisabledCgroup tells that case apart.
//
// debugAllowSwap is the operator's explicit opt-out, surfaced as
// the --debug-allow-swap CLI flag and resolved by the caller. The
// caller emits a Warning annotation to make the choice visible in
// CI logs.
//
// The fix on a real swap-enabled host is one of:
//
//   - disable swap on the runner (`sudo swapoff -a`); a hosted runner
//     needs it too where swap is on, as on GitHub's, and the GitHub
//     release layer does it before signing
//   - run the job container with swap disabled (memory-swap equal to
//     memory) in the runner's setup
//   - use a signing method that doesn't decrypt a long-lived key in
//     this process (--method=sigstore or --method=kms)
//
// Reports which of the passing states applied, so the caller can tell
// the operator apart from the log:
//
//   - SwapAbsent: swap state was read and no swap area is active.
//   - SwapCgroupDisabled: a swap area is active, but memory.swap.max is 0
//     for the returned cgroup directory, this process's or an ancestor's.
//   - SwapBypassed: swap is active and debugAllowSwap waived it (operator
//     opt-out). Only reported when a waiver actually mattered.
//   - SwapOverridden: no swap area was reported by the FORGE_CI_PROC_SWAPS
//     file rather than by /proc/swaps.
//   - SwapUndetermined: /proc/swaps is unreadable — we can't assert swap is
//     on without positive evidence, and refusing would block legitimate
//     restricted-container deployments where the indeterminacy is
//     structural. The policy did not run; the caller says so.
//
// The cgroup is returned only with SwapCgroupDisabled. It is not consulted
// when FORGE_CI_PROC_SWAPS reports swap: an injected answer stays the answer.
//
// Returns errs.ErrInvalidConfig (exit code EX_CONFIG = 78) when
// swap can be used and debugAllowSwap is false.
func RequireNoSwap(debugAllowSwap bool) (SwapState, string, error) {
	on, err := SwapEnabled()
	if err != nil {
		// Deliberate fail-open: when /proc/swaps isn't readable
		// (containerless test runs, locked-down jail, /proc not mounted)
		// we can't determine swap state. The defensive posture would be
		// to block, but that would refuse to run a great many legitimate
		// CI setups for a check that is a defence-in-depth layer. Nothing
		// else keeps key material out of swap: the process hardening
		// (RLIMIT_CORE, PR_SET_DUMPABLE) covers core files and debuggers, and
		// no memory is mlocked. Adopters in strict-posture environments can
		// surface the underlying error via a separate `SwapEnabled()`
		// call if they want hard guarantees.
		return SwapUndetermined, "", nil //nolint:nilerr // see comment above
	}

	overridden := os.Getenv(procSwapsOverrideEnv) != ""

	if !on {
		if overridden {
			return SwapOverridden, "", nil
		}

		return SwapAbsent, "", nil
	}

	if !overridden {
		if cgroup, ok := swapDisabledCgroup(); ok {
			return SwapCgroupDisabled, cgroup, nil
		}
	}

	// Swap is usable. The operator's explicit waiver is consulted only now,
	// so --debug-allow-swap on a swap-free runner is not reported as a
	// bypass: nothing was waived, and a loud warning there would train
	// operators to ignore the one that matters.
	if debugAllowSwap {
		return SwapBypassed, "", nil
	}

	return SwapAbsent, "", fmt.Errorf(
		"swap is enabled on this runner; refusing to handle private-key material to prevent swap-page extraction.\n\n"+
			"Fix the runner:\n"+
			"  • disable swap on the host: `sudo swapoff -a` (persist via /etc/fstab)\n"+
			"  • or run the job with swap disabled for its container, memory-swap equal to memory:\n"+
			"    Docker/Podman `--memory 4g --memory-swap 4g`; Forgejo runner `container.options`;\n"+
			"    GitLab runner `[runners.docker]` `memory` and `memory_swap`\n"+
			"  • a hosted runner is no exception: GitHub's have swap on, and the GitHub release layer\n"+
			"    turns it off in the signing job; on your own runners configure it in the runner setup\n\n"+
			"Or use forge-ci release sign with a backend that does not load a long-lived private key into this process:\n"+
			"  --method=sigstore: Sigstore keyless via cosign and OIDC (ephemeral keys are still handled by cosign)\n"+
			"  --method=kms: KMS-backed signing with a remote key reference (a local key file is not remote KMS)\n"+
			"  Backend credentials and subprocess key handling still require a trusted runner.\n\n"+
			"Debug-only escape hatch (LOCAL USE ONLY — NOT for production releases):\n"+
			"  forge-ci release sign --debug-allow-swap\n"+
			"  When set, the policy is bypassed and a Warning annotation is emitted.\n\n"+
			"Background: %s: %w",
		docslink.URL("verification.md#swap-policy"), errs.ErrInvalidConfig,
	)
}
