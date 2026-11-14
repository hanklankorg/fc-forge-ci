// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package safeexec_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/safeexec"
)

// writeProcSwaps creates a fake /proc/swaps body and points the kernel reader
// at it, with the override variable cleared so the result is kernel state. The
// cgroup reader points at nothing, so the test host's own cgroup cannot pass a
// swap-enabled fixture.
func writeProcSwaps(t *testing.T, body string) {
	t.Helper()

	t.Setenv("FORGE_CI_PROC_SWAPS", "")
	safeexec.UseProcSwapsFixture(t, writeSwapsFile(t, body))

	missing := filepath.Join(t.TempDir(), "missing")
	safeexec.UseCgroupFixture(t, missing, missing)
}

func writeSwapsFile(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "swaps")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestSwapEnabled_HeaderOnlyIsFalse(t *testing.T) {
	writeProcSwaps(t, "Filename\tType\tSize\tUsed\tPriority\n")

	on, err := safeexec.SwapEnabled()
	if err != nil {
		t.Fatal(err)
	}

	if on {
		t.Errorf("header-only /proc/swaps must report swap off")
	}
}

func TestSwapEnabled_ActiveSwapAreaIsTrue(t *testing.T) {
	writeProcSwaps(t, `Filename	Type	Size	Used	Priority
/dev/sda2	partition	2097148	0	-2
`)

	on, err := safeexec.SwapEnabled()
	if err != nil {
		t.Fatal(err)
	}

	if !on {
		t.Errorf("active swap area must report swap on")
	}
}

func TestSwapEnabled_ReadErrorIsIndeterminate(t *testing.T) {
	t.Setenv("FORGE_CI_PROC_SWAPS", "")
	safeexec.UseProcSwapsFixture(t, filepath.Join(t.TempDir(), "does-not-exist"))

	on, err := safeexec.SwapEnabled()
	if err == nil {
		t.Fatal("expected non-nil error on missing /proc/swaps")
	}

	if on {
		t.Errorf("indeterminate state must NOT claim swap on")
	}
}

func TestRequireNoSwap_RefusesOnSwapOn(t *testing.T) {
	writeProcSwaps(t, `Filename	Type	Size	Used	Priority
/dev/sda2	partition	2097148	0	-2
`)

	state, _, err := safeexec.RequireNoSwap(false)
	if err == nil {
		t.Fatal("expected refusal on swap-enabled host")
	}

	if state != safeexec.SwapAbsent {
		t.Errorf("a refusal must not report a passing state; got %s", state)
	}

	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig (exit EX_CONFIG=78), got %v", err)
	}

	for _, want := range []string{"swapoff", "memory-swap equal to memory", "container.options", "memory_swap", "hosted runner", "--method=sigstore", "--method=kms", "ephemeral keys", "local key file is not remote KMS", "--debug-allow-swap"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message must mention %q for operator guidance; got:\n%s", want, err.Error())
		}
	}

	if strings.Contains(err.Error(), "does not yet provide") || strings.Contains(err.Error(), "outside forge-ci") {
		t.Fatalf("stale backend guidance: %v", err)
	}
}

func TestRequireNoSwap_PassesWhenSwapOff(t *testing.T) {
	writeProcSwaps(t, "Filename\tType\tSize\tUsed\tPriority\n")

	state, _, err := safeexec.RequireNoSwap(false)
	if err != nil {
		t.Errorf("expected nil on swap-off host, got %v", err)
	}

	// SwapAbsent, not SwapUndetermined: the file was read and reported no
	// swap area. The caller stays silent only for this state, so conflating
	// it with "we could not look" would put a notice on every clean run.
	if state != safeexec.SwapAbsent {
		t.Errorf("swap-off host reported %s, want absent", state)
	}
}

func TestRequireNoSwap_SoftPassesOnReadError(t *testing.T) {
	// Indeterminate state — refusing here would block legitimate
	// restricted-container deployments where /proc/swaps is hidden.
	t.Setenv("FORGE_CI_PROC_SWAPS", "")
	safeexec.UseProcSwapsFixture(t, filepath.Join(t.TempDir(), "does-not-exist"))

	state, _, err := safeexec.RequireNoSwap(false)
	if err != nil {
		t.Errorf("expected soft-pass on indeterminate state, got %v", err)
	}

	// The soft-pass must be distinguishable from a clean run: this is the
	// bypass the operator did not choose, and the CLI turns this state into
	// the Notice that says the policy did not run.
	if state != safeexec.SwapUndetermined {
		t.Errorf("unreadable /proc/swaps reported %s, want undetermined — the skip would pass silently", state)
	}
}

// TestRequireNoSwap_DebugOverridePasses pins the documented escape
// hatch: when the caller passes debugAllowSwap=true (sourced from
// the --debug-allow-swap CLI flag), the policy passes.
func TestRequireNoSwap_DebugOverridePasses(t *testing.T) {
	writeProcSwaps(t, `Filename	Type	Size	Used	Priority
/dev/sda2	partition	2097148	0	-2
`)

	state, _, err := safeexec.RequireNoSwap(true)
	if err != nil {
		t.Errorf("debug override must bypass refusal, got %v", err)
	}

	if state != safeexec.SwapBypassed {
		t.Errorf("waived an active swap area but reported %s, want bypassed", state)
	}
}

// TestRequireNoSwap_DebugOverrideOnSwapFreeHostIsNotABypass: --debug-allow-swap
// on a host with no swap waives nothing, so it must not be reported as a
// bypass. The caller renders SwapBypassed as a loud "DO NOT USE FOR PRODUCTION"
// warning; emitting that when the policy was satisfied on its own terms trains
// operators to ignore the warning that matters.
func TestRequireNoSwap_DebugOverrideOnSwapFreeHostIsNotABypass(t *testing.T) {
	writeProcSwaps(t, "Filename\tType\tSize\tUsed\tPriority\n")

	state, _, err := safeexec.RequireNoSwap(true)
	if err != nil {
		t.Fatalf("swap-off host with the override set must pass, got %v", err)
	}

	if state != safeexec.SwapAbsent {
		t.Errorf("override on a swap-free host reported %s, want absent (nothing was waived)", state)
	}
}

// TestRequireNoSwap_OverrideIsReportedNotTakenAsKernelState sets
// FORGE_CI_PROC_SWAPS while the kernel reader points at a file with an
// active swap area. The override decides the answer, as the black-box suite
// needs, but a pass read from it is SwapOverridden rather than SwapAbsent, so
// fixture bytes are never reported as measured kernel state. An override that
// reports swap still refuses, and an unreadable override is undetermined.
func TestRequireNoSwap_OverrideIsReportedNotTakenAsKernelState(t *testing.T) {
	kernelOn := writeSwapsFile(t, "Filename\tType\tSize\tUsed\tPriority\n/dev/sda2\tpartition\t2097148\t0\t-2\n")

	for _, tc := range []struct {
		name      string
		override  string
		wantState safeexec.SwapState
		wantErr   error
	}{
		{"override reports no swap", writeSwapsFile(t, "Filename\tType\tSize\tUsed\tPriority\n"), safeexec.SwapOverridden, nil},
		{"empty override file", writeSwapsFile(t, ""), safeexec.SwapOverridden, nil},
		{"override reports swap", kernelOn, safeexec.SwapAbsent, errs.ErrInvalidConfig},
		{"unreadable override", filepath.Join(t.TempDir(), "missing"), safeexec.SwapUndetermined, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			safeexec.UseProcSwapsFixture(t, kernelOn)
			t.Setenv("FORGE_CI_PROC_SWAPS", tc.override)

			state, _, err := safeexec.RequireNoSwap(false)
			if state != tc.wantState || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("state = %s, err = %v; want %s, %v", state, err, tc.wantState, tc.wantErr)
			}
		})
	}
}

// TestRequireNoSwap_CgroupSwapLimit: /proc/swaps is host-wide, so a job
// container sees the host's swap even when its cgroup forbids swap. Only
// memory.swap.max of exactly 0 on the process's cgroup or an ancestor passes;
// "max", a non-zero limit, missing files or cgroup v1 still refuse.
func TestRequireNoSwap_CgroupSwapLimit(t *testing.T) {
	const (
		header = "Filename\tType\tSize\tUsed\tPriority\n"
		active = header + "/dev/sda2\tpartition\t2097148\t0\t-2\n"
		job    = "0::/runner.slice/job.scope\n"
	)

	for _, tc := range []struct {
		name       string
		swaps      string
		selfCgroup string            // body of /proc/self/cgroup; "" leaves it missing
		limits     map[string]string // memory.swap.max per directory below the root
		wantState  safeexec.SwapState
		wantCgroup string
		wantErr    error
	}{
		{"no swap area", header, job, map[string]string{"runner.slice/job.scope": "max"}, safeexec.SwapAbsent, "", nil},
		{"own cgroup forbids swap", active, job, map[string]string{"runner.slice/job.scope": "0\n", "runner.slice": "max"}, safeexec.SwapCgroupDisabled, "runner.slice/job.scope", nil},
		{"ancestor forbids swap", active, job, map[string]string{"runner.slice/job.scope": "max", "runner.slice": "0\n"}, safeexec.SwapCgroupDisabled, "runner.slice", nil},
		{"ancestor above a missing file", active, job, map[string]string{"runner.slice": "0\n"}, safeexec.SwapCgroupDisabled, "runner.slice", nil},
		{"cgroup namespace root", active, "0::/\n", map[string]string{".": "0\n"}, safeexec.SwapCgroupDisabled, ".", nil},
		{"swap allowed everywhere", active, job, map[string]string{"runner.slice/job.scope": "max", "runner.slice": "max"}, safeexec.SwapAbsent, "", errs.ErrInvalidConfig},
		{"non-zero limit", active, job, map[string]string{"runner.slice/job.scope": "4096\n"}, safeexec.SwapAbsent, "", errs.ErrInvalidConfig},
		{"no memory.swap.max files", active, job, nil, safeexec.SwapAbsent, "", errs.ErrInvalidConfig},
		{"no /proc/self/cgroup", active, "", map[string]string{".": "0\n"}, safeexec.SwapAbsent, "", errs.ErrInvalidConfig},
		{"cgroup v1 only", active, "4:memory:/job\n", map[string]string{".": "0\n", "job": "0\n"}, safeexec.SwapAbsent, "", errs.ErrInvalidConfig},
		{"path outside the cgroup namespace", active, "0::/../x\n", map[string]string{"../x": "0\n", "x": "0\n"}, safeexec.SwapAbsent, "", errs.ErrInvalidConfig},
		{"hybrid v1 lines beside v2", active, "4:memory:/x\n1:name=systemd:/x\n0::/x\n", map[string]string{"x": "0\n"}, safeexec.SwapCgroupDisabled, "x", nil},
		{"zero only on a sibling", active, job, map[string]string{"runner.slice/job.scope": "max", "runner.slice/other.scope": "0\n", "runner.slice": "max"}, safeexec.SwapAbsent, "", errs.ErrInvalidConfig},
		{"empty v2 path", active, "0::\n", map[string]string{".": "0\n"}, safeexec.SwapAbsent, "", errs.ErrInvalidConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FORGE_CI_PROC_SWAPS", "")
			safeexec.UseProcSwapsFixture(t, writeSwapsFile(t, tc.swaps))

			root := filepath.Join(t.TempDir(), "cgroup")
			for dir, limit := range tc.limits {
				writeFixtureFile(t, filepath.Join(root, dir, "memory.swap.max"), limit)
			}

			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}

			selfCgroup := filepath.Join(t.TempDir(), "cgroup")
			if tc.selfCgroup != "" {
				writeFixtureFile(t, selfCgroup, tc.selfCgroup)
			}

			safeexec.UseCgroupFixture(t, selfCgroup, root)

			wantCgroup := ""
			if tc.wantCgroup != "" {
				wantCgroup = filepath.Join(root, tc.wantCgroup)
			}

			state, cgroup, err := safeexec.RequireNoSwap(false)
			if state != tc.wantState || cgroup != wantCgroup || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("state = %s, cgroup = %q, err = %v; want %s, %q, %v", state, cgroup, err, tc.wantState, wantCgroup, tc.wantErr)
			}
		})
	}
}

// TestRequireNoSwap_OverrideIgnoresCgroup: an injected answer that reports
// swap refuses even where the cgroup forbids swap, so the black-box suite's
// refusal does not depend on the host that runs it.
func TestRequireNoSwap_OverrideIgnoresCgroup(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, filepath.Join(root, "memory.swap.max"), "0\n")

	selfCgroup := filepath.Join(t.TempDir(), "cgroup")
	writeFixtureFile(t, selfCgroup, "0::/\n")
	safeexec.UseCgroupFixture(t, selfCgroup, root)
	t.Setenv("FORGE_CI_PROC_SWAPS", writeSwapsFile(t, "Filename\tType\tSize\tUsed\tPriority\n/dev/sda2\tpartition\t1\t0\t-2\n"))

	if state, _, err := safeexec.RequireNoSwap(false); !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("override reporting swap passed as %s, %v; want the refusal", state, err)
	}
}

func writeFixtureFile(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
