// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestGitLabHandoffValidatesAcrossDifferentWorkspaces(t *testing.T) {
	t.Parallel()
	engine := forgeCIEngine(t)

	for _, tampered := range []bool{false, true} {
		t.Run(map[bool]string{false: "intact", true: "tampered"}[tampered], func(t *testing.T) {
			t.Parallel()

			producer, receiver := t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(producer, "dist"), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(producer, "dist", "payload"), []byte("artifact bytes"), 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()

			script := `source "$1"
rc_engine release dist-digest --dist-dir "$PRODUCER/dist" --manifest-root . > "$PRODUCER/digest"
cp -R "$PRODUCER" "$RECEIVER/handoff"
if [ "$TAMPERED" = true ]; then printf changed > "$RECEIVER/handoff/dist/payload"; fi
umask 077
rc_read_handoff "$RECEIVER/handoff"
`
			command := exec.CommandContext(ctx, "/bin/bash", "--noprofile", "--norc", "-eu", "-c", script, "handoff", filepath.Join(reporoot.Path(t), ".gitlab/bin/release.sh")) //nolint:gosec // Actual orchestration and built engine, on inert files only.
			command.Dir = receiver
			flag := "false"

			if tampered {
				flag = "true"
			}

			command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + receiver, "PRODUCER=" + producer, "RECEIVER=" + receiver,
				"FORGE_CI_BIN=" + engine, "TAMPERED=" + flag}
			output, err := command.CombinedOutput()

			if (err != nil) != tampered {
				t.Fatalf("handoff tampered=%t, error=%v\n%s", tampered, err, output)
			}

			copied := filepath.Join(receiver, "dist", "payload")
			if tampered {
				if _, err := os.Stat(copied); !os.IsNotExist(err) {
					t.Fatal("tampered handoff entered the private checkout")
				}
			} else if body, err := os.ReadFile(copied); err != nil || string(body) != "artifact bytes" {
				t.Fatalf("cross-workspace handoff lost its bytes: %v", err)
			}
		})
	}
}
