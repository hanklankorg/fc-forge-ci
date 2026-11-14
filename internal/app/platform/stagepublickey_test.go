// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appplatform "github.com/diggsweden/forge-ci/internal/app/platform"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

const publicKeyBody = "-----BEGIN PUBLIC KEY-----\nFIXTURE\n-----END PUBLIC KEY-----\n"

// stageFixture writes a source key and its sidecar, and returns the key
// directory, the workspace and the key's digest.
func stageFixture(t *testing.T) (string, string, string) {
	t.Helper()

	keyDir, workspace := t.TempDir(), t.TempDir()
	sum := sha256.Sum256([]byte(publicKeyBody))
	digest := hex.EncodeToString(sum[:])

	write(t, filepath.Join(keyDir, "cosign.pub"), publicKeyBody)
	write(t, filepath.Join(keyDir, "cosign.pub.sha256"), digest+"  keys/cosign.pub\n")

	return keyDir, workspace, digest
}

func write(t *testing.T, path, body string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// recordingSink captures the outputs a command publishes.
type recordingSink struct{ values map[string]string }

func (s *recordingSink) Set(_ context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}

	s.values[key] = value

	return nil
}

func (s *recordingSink) SetBool(context.Context, string, bool) error          { return nil }
func (s *recordingSink) SetMultiline(context.Context, string, []string) error { return nil }
func (s *recordingSink) Close(context.Context) error                          { return nil }

func stage(t *testing.T, sink *recordingSink, in appplatform.StagePublicKeyInput) error {
	t.Helper()

	return appplatform.StagePublicKey(t.Context(), sink, io.Discard, in)
}

// A staged key is the source byte for byte, world-readable for the later
// verification step, and its digest is published for the caller to bind.
func TestStagePublicKey_StagesTheKeyAndPublishesItsDigest(t *testing.T) {
	t.Parallel()

	keyDir, workspace, digest := stageFixture(t)
	sink := &recordingSink{}

	err := stage(t, sink, appplatform.StagePublicKeyInput{
		Source:      filepath.Join(keyDir, "cosign.pub"),
		DigestFile:  filepath.Join(keyDir, "cosign.pub.sha256"),
		Destination: "trust/keys/cosign.pub",
		Workspace:   workspace,
	})
	if err != nil {
		t.Fatalf("staging a reviewed key failed: %v", err)
	}

	staged := filepath.Join(workspace, "trust", "keys", "cosign.pub")

	body, readErr := os.ReadFile(staged)
	if readErr != nil || string(body) != publicKeyBody {
		t.Fatalf("staged key = %q (%v)", body, readErr)
	}

	info, statErr := os.Lstat(staged)
	if statErr != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("staged mode = %v (%v)", info.Mode().Perm(), statErr)
	}

	if sink.values["sha256"] != digest {
		t.Errorf("published digest = %q, want %q", sink.values["sha256"], digest)
	}

	if entries, _ := os.ReadDir(filepath.Dir(staged)); len(entries) != 1 {
		t.Errorf("staging left %d entries beside the key", len(entries)-1)
	}
}

// The destination is confined to the workspace, and the key itself must be the
// reviewed file rather than a link to another one.
func TestStagePublicKey_RefusesEscapesAndLinks(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		destination string
		prepare     func(t *testing.T, keyDir, workspace string) string
		wantErr     error
	}{
		{name: "absolute destination", destination: "/etc/cosign.pub", wantErr: errs.ErrUsage},
		{name: "parent traversal", destination: "../outside/cosign.pub", wantErr: errs.ErrUsage},
		{name: "workspace itself", destination: ".", wantErr: errs.ErrUsage},
		{name: "newline in destination", destination: "keys/cosign.pub\nx", wantErr: errs.ErrUsage},
		{
			name: "symlinked parent leaving the workspace", destination: "escape/cosign.pub",
			prepare: func(t *testing.T, _, workspace string) string {
				t.Helper()

				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
					t.Fatal(err)
				}

				return outside
			},
		},
		{
			name: "symlinked source key", destination: "cosign.pub",
			prepare: func(t *testing.T, keyDir, _ string) string {
				t.Helper()

				if err := os.Rename(filepath.Join(keyDir, "cosign.pub"), filepath.Join(keyDir, "real.pub")); err != nil {
					t.Fatal(err)
				}

				if err := os.Symlink(filepath.Join(keyDir, "real.pub"), filepath.Join(keyDir, "cosign.pub")); err != nil {
					t.Fatal(err)
				}

				return ""
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			keyDir, workspace, _ := stageFixture(t)

			outside := ""
			if test.prepare != nil {
				outside = test.prepare(t, keyDir, workspace)
			}

			err := stage(t, &recordingSink{}, appplatform.StagePublicKeyInput{
				Source:      filepath.Join(keyDir, "cosign.pub"),
				DigestFile:  filepath.Join(keyDir, "cosign.pub.sha256"),
				Destination: test.destination,
				Workspace:   workspace,
			})
			if err == nil {
				t.Fatal("unsafe staging was accepted")
			}

			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("err = %v, want %v", err, test.wantErr)
			}

			if outside != "" {
				if entries, _ := os.ReadDir(outside); len(entries) != 0 {
					t.Errorf("staging wrote %d entries outside the workspace", len(entries))
				}
			}
		})
	}
}

// A key that does not match its sidecar is refused, and whatever the
// destination already held is left alone.
func TestStagePublicKey_RefusesADigestMismatchAndKeepsTheExistingFile(t *testing.T) {
	t.Parallel()

	keyDir, workspace, _ := stageFixture(t)
	write(t, filepath.Join(keyDir, "cosign.pub"), "replaced after review\n")
	write(t, filepath.Join(workspace, "cosign.pub"), "previous\n")

	err := stage(t, &recordingSink{}, appplatform.StagePublicKeyInput{
		Source:      filepath.Join(keyDir, "cosign.pub"),
		DigestFile:  filepath.Join(keyDir, "cosign.pub.sha256"),
		Destination: "cosign.pub",
		Workspace:   workspace,
	})
	if !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}

	body, _ := os.ReadFile(filepath.Join(workspace, "cosign.pub"))
	if string(body) != "previous\n" {
		t.Fatalf("existing file = %q, want it untouched", body)
	}

	if entries, _ := os.ReadDir(workspace); len(entries) != 1 {
		t.Errorf("a refused staging left %d entries", len(entries))
	}
}

// The sidecar must hold exactly one sha256sum row, and the key must be a
// non-empty regular file.
func TestStagePublicKey_RefusesAMalformedSidecarOrKey(t *testing.T) {
	t.Parallel()

	const keep = "\x00keep"

	for _, test := range []struct {
		name, digestFile, key string
	}{
		{name: "two rows", digestFile: strings.Repeat("a", 64) + "  a\n" + strings.Repeat("b", 64) + "  b\n", key: keep},
		{name: "not hexadecimal", digestFile: strings.Repeat("z", 64) + "  keys/cosign.pub\n", key: keep},
		{name: "uppercase digest", digestFile: strings.Repeat("A", 64) + "  keys/cosign.pub\n", key: keep},
		{name: "short digest", digestFile: strings.Repeat("a", 63) + "  keys/cosign.pub\n", key: keep},
		{name: "empty sidecar", digestFile: "", key: keep},
		{name: "empty key", digestFile: keep, key: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			keyDir, workspace, _ := stageFixture(t)
			if test.digestFile != keep {
				write(t, filepath.Join(keyDir, "cosign.pub.sha256"), test.digestFile)
			}

			if test.key != keep {
				write(t, filepath.Join(keyDir, "cosign.pub"), test.key)
			}

			err := stage(t, &recordingSink{}, appplatform.StagePublicKeyInput{
				Source:      filepath.Join(keyDir, "cosign.pub"),
				DigestFile:  filepath.Join(keyDir, "cosign.pub.sha256"),
				Destination: "cosign.pub",
				Workspace:   workspace,
			})
			if !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("err = %v, want ErrValidation", err)
			}
		})
	}
}

// A destination that is already a symlink is replaced by the key itself, and
// the link target is left untouched.
func TestStagePublicKey_ReplacesASymlinkedDestination(t *testing.T) {
	t.Parallel()

	keyDir, workspace, _ := stageFixture(t)
	target := filepath.Join(t.TempDir(), "target")
	write(t, target, "target body\n")

	if err := os.Symlink(target, filepath.Join(workspace, "cosign.pub")); err != nil {
		t.Fatal(err)
	}

	err := stage(t, &recordingSink{}, appplatform.StagePublicKeyInput{
		Source:      filepath.Join(keyDir, "cosign.pub"),
		DigestFile:  filepath.Join(keyDir, "cosign.pub.sha256"),
		Destination: "cosign.pub",
		Workspace:   workspace,
	})
	if err != nil {
		t.Fatalf("staging over a symlink failed: %v", err)
	}

	info, statErr := os.Lstat(filepath.Join(workspace, "cosign.pub"))
	if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("destination is still a symlink (%v)", statErr)
	}

	body, _ := os.ReadFile(target)
	if string(body) != "target body\n" {
		t.Fatalf("link target = %q, want it untouched", body)
	}
}
