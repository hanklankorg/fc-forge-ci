// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func protectedSigningTestDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestReusedSigningInputsRefuseEveryMissingOrEmptyInputWithoutCreatingFiles(t *testing.T) {
	for _, name := range []string{"lab-cosign.key", "lab-cosign.pub", "lab-cosign.passphrase"} {
		for _, empty := range []bool{false, true} {
			dir := protectedSigningTestDir(t)

			for _, file := range []string{"lab-cosign.key", "lab-cosign.pub", "lab-cosign.passphrase"} {
				if file == name && !empty {
					continue
				}

				body := []byte("inert metadata-only fixture")
				if file == name {
					body = nil
				}

				if err := os.WriteFile(filepath.Join(dir, file), body, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if err := CheckReusedSigningInputs(dir); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("%s empty=%t: %v", name, empty, err)
			}

			if !empty {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatal("missing input was generated")
				}
			}
		}
	}
}

func TestSigningInputRefusalPrecedesContractLoading(t *testing.T) {
	t.Setenv("FORGE_CI_LIVE_SIGNING_KEY_DIR", protectedSigningTestDir(t))
	t.Setenv("LAB_TARGETS_FILE", filepath.Join(t.TempDir(), "absent-contract.json"))

	_, err := CheckLiveInputs(time.Now())
	if err == nil || !strings.Contains(err.Error(), "lab-cosign.key") {
		t.Fatalf("signing preflight did not run first: %v", err)
	}
}

func TestReusedSigningInputsAcceptProtectedFilesButRejectExposedPrivateMaterial(t *testing.T) {
	dir := protectedSigningTestDir(t)
	for _, name := range []string{"lab-cosign.key", "lab-cosign.pub", "lab-cosign.passphrase"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("inert metadata-only fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := CheckReusedSigningInputs(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(filepath.Join(dir, "lab-cosign.key"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CheckReusedSigningInputs(dir); err == nil {
		t.Fatal("exposed private input admitted")
	}
}
