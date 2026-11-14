//go:build integration

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gpg_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	adaptergpg "github.com/diggsweden/forge-ci/internal/adapters/gpg"
	domaingpg "github.com/diggsweden/forge-ci/internal/domain/gpg"
	"github.com/diggsweden/forge-ci/internal/testutil/gpgkey"
)

// TestImportKey_RoundTripWithRealGPG covers the on-disk-keyring import
// path that git's own `tag -s`/`commit -S` depends on. We verify success
// via ListKeygrips (the agent-integration entry point that actually
// matters downstream) rather than a separate fingerprint round-trip —
// the parsing-only `--list-secret-keys` shellout has been retired in
// favour of in-process metadata extraction via adapters/openpgp.
func TestImportKey_RoundTripWithRealGPG(t *testing.T) {
	signingKey := gpgkey.New(t)
	armored := signingKey.ArmoredPrivateKey()

	adapter := adaptergpg.New()
	ctx := context.Background()

	adapter.DeleteSecretKey(ctx, signingKey.Fingerprint)
	adapter.DeleteKey(ctx, signingKey.Fingerprint)

	if err := adapter.ImportKey(ctx, []byte(armored)); err != nil {
		t.Fatalf("ImportKey: %v", err)
	}

	grips, err := adapter.ListKeygrips(ctx, signingKey.Fingerprint)
	if err != nil || !strings.Contains(grips, "grp:") {
		t.Errorf("post-import ListKeygrips = %q, err = %v; want non-empty grp lines", grips, err)
	}
}

func TestListKeygrips_NonEmpty(t *testing.T) {
	signingKey := gpgkey.New(t)
	adapter := adaptergpg.New()

	out, err := adapter.ListKeygrips(context.Background(), signingKey.Fingerprint)
	if err != nil {
		t.Fatalf("ListKeygrips: %v", err)
	}

	grips := domaingpg.ParseKeygrips(out)
	if len(grips) == 0 {
		t.Fatal("expected at least one keygrip")
	}

	for _, g := range grips {
		if len(g) != 40 || !isHex(g) {
			t.Errorf("keygrip %q is not a 40-char hex string", g)
		}
	}
}

func TestConfigureAgent_WritesConfFile(t *testing.T) {
	signingKey := gpgkey.New(t)
	adapter := adaptergpg.New()

	if err := adapter.ConfigureAgent(context.Background()); err != nil {
		t.Fatalf("ConfigureAgent: %v", err)
	}

	confPath := filepath.Join(signingKey.GNUPGHOME, "gpg-agent.conf")

	data, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read gpg-agent.conf: %v", err)
	}

	for _, want := range []string{"default-cache-ttl 21600", "max-cache-ttl 31536000", "allow-preset-passphrase"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("gpg-agent.conf missing %q\nfull:\n%s", want, data)
		}
	}
}

// TestPresetPassphrase_UnlocksSigningWithoutPinentry is what release
// signing relies on: after the preset, git's gpg signs a protected key with
// no pinentry. The fakes only saw the preset's argv; a preset gpg-connect-agent
// never sent still exited 0, and CI signing failed asking for a pinentry.
func TestPresetPassphrase_UnlocksSigningWithoutPinentry(t *testing.T) {
	const passphrase = "correct horse battery staple"

	signingKey := gpgkey.NewProtected(t, passphrase)
	adapter := adaptergpg.New()

	payload := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(payload, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}

	sign := func() ([]byte, error) {
		return exec.CommandContext(t.Context(), "gpg", "--homedir", signingKey.GNUPGHOME, "--batch", "--yes",
			"--pinentry-mode", "error", "--local-user", signingKey.Fingerprint,
			"--output", payload+".sig", "--detach-sign", payload).CombinedOutput()
	}

	if err := adapter.ConfigureAgent(t.Context()); err != nil {
		t.Fatalf("ConfigureAgent: %v", err)
	}

	if out, err := sign(); err == nil {
		t.Fatalf("the protected key signed before any preset, so this test proves nothing:\n%s", out)
	}

	out, err := adapter.ListKeygrips(t.Context(), signingKey.Fingerprint)
	if err != nil {
		t.Fatalf("ListKeygrips: %v", err)
	}

	for _, grip := range domaingpg.ParseKeygrips(out) {
		if err := adapter.PresetPassphrase(t.Context(), grip, passphrase); err != nil {
			t.Fatalf("PresetPassphrase(%s): %v", grip, err)
		}
	}

	if out, err := sign(); err != nil {
		t.Fatalf("signing after the preset needed a pinentry: %v\n%s", err, out)
	}
}

// TestDelete_RemovesTheKeyAndRepeatsHarmlessly covers both halves. The
// deletes return nothing and only report gpg's errors, so "it did not blow
// up" is not evidence of anything: what has to hold is that the key is
// actually gone afterwards, and that a second pass over an already-empty
// keyring still leaves it that way.
func TestDelete_RemovesTheKeyAndRepeatsHarmlessly(t *testing.T) {
	signingKey := gpgkey.New(t)
	adapter := adaptergpg.New()
	ctx := context.Background()

	// The key is there to begin with, or nothing below is a deletion.
	before, err := adapter.ListKeygrips(ctx, signingKey.Fingerprint)
	if err != nil || len(domaingpg.ParseKeygrips(before)) == 0 {
		t.Fatalf("fixture key is not in the keyring: grips=%q err=%v", before, err)
	}

	// First round removes the throwaway key.
	adapter.DeleteSecretKey(ctx, signingKey.Fingerprint)
	adapter.DeleteKey(ctx, signingKey.Fingerprint)

	assertNoKeygrips(t, adapter, signingKey.Fingerprint, "after the first delete")

	// Second round: keys are gone — the repeat must not resurrect or
	// error out, which for a void, best-effort call means the
	// keyring has to still be empty.
	adapter.DeleteSecretKey(ctx, signingKey.Fingerprint)
	adapter.DeleteKey(ctx, signingKey.Fingerprint)

	assertNoKeygrips(t, adapter, signingKey.Fingerprint, "after the repeated delete")

	// Never-existed fingerprint.
	adapter.DeleteSecretKey(ctx, "0000000000000000000000000000000000000000")
	adapter.DeleteKey(ctx, "0000000000000000000000000000000000000000")

	assertNoKeygrips(t, adapter, signingKey.Fingerprint, "after deleting an unrelated fingerprint")
}

// assertNoKeygrips fails unless the fingerprint resolves to no keygrips.
// gpg reports an unknown key as an error rather than an empty listing, so
// either outcome counts as absent.
func assertNoKeygrips(t *testing.T, a *adaptergpg.Adapter, fingerprint, when string) {
	t.Helper()

	out, err := a.ListKeygrips(context.Background(), fingerprint)
	if err == nil && len(domaingpg.ParseKeygrips(out)) != 0 {
		t.Errorf("%s the key is still in the keyring: %q", when, out)
	}
}

func isHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}

	return true
}
