// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform_test

import (
	"os"
	"path/filepath"
	"testing"

	appplatform "github.com/diggsweden/forge-ci/internal/app/platform"
	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
)

func TestPublicKeyRestrictiveUmask(t *testing.T) {
	evidence.UnderUmask(t, "^TestStagePublicKey_")
}

func TestStagePublicKey_RefusesPreexistingStagingEntries(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			keyDir, workspace, _ := stageFixture(t)
			canary := filepath.Join(t.TempDir(), "private")
			write(t, canary, "private canary")
			staging := filepath.Join(workspace, "cosign.pub.staging")
			var err error
			switch kind {
			case "regular":
				write(t, staging, "private staging canary")
			case "symlink":
				err = os.Symlink(canary, staging)
			case "hardlink":
				err = os.Link(canary, staging)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = stage(t, &recordingSink{}, appplatform.StagePublicKeyInput{
				Source: filepath.Join(keyDir, "cosign.pub"), DigestFile: filepath.Join(keyDir, "cosign.pub.sha256"),
				Destination: "cosign.pub", Workspace: workspace,
			})
			if err == nil {
				t.Fatal("preexisting staging entry accepted")
			}
			body, err := os.ReadFile(canary)
			if err != nil || string(body) != "private canary" {
				t.Fatalf("private canary changed: %q, %v", body, err)
			}
			info, err := os.Stat(canary)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("private canary mode changed: %v, %v", info, err)
			}
			if _, err := os.Lstat(staging); err != nil {
				t.Fatalf("foreign staging entry removed: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(workspace, "cosign.pub")); !os.IsNotExist(err) {
				t.Fatalf("destination created: %v", err)
			}
		})
	}
}
