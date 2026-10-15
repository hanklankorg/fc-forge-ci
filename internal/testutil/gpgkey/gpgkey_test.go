//go:build integration

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gpgkey_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/openpgp"
	"github.com/diggsweden/forge-ci/internal/testutil/gpgkey"
)

// TestArmoredKeys_ParseAsTheKeyTheyClaimToBe parses the armor, not its headers.
// Substring assertions on BEGIN/END lines are satisfied by two header lines
// with nothing between them, and by a public block mislabelled as a private
// one — both of which would make every downstream signing fixture fail in a
// way that points at the code under test rather than at the fixture.
//
// The parser used here is the one production uses on this same armor, so what
// this proves is that the fixture is readable by the code it feeds, not merely
// that gpg emitted something.
func TestArmoredKeys_ParseAsTheKeyTheyClaimToBe(t *testing.T) {
	k := gpgkey.New(t)

	for name, armor := range map[string]string{
		"private": k.ArmoredPrivateKey(),
		"public":  k.ArmoredPublicKey(),
	} {
		t.Run(name, func(t *testing.T) {
			md, err := openpgp.ReadMetadata([]byte(armor))
			if err != nil {
				t.Fatalf("parse armored %s key: %v", name, err)
			}

			if md.Fingerprint != k.Fingerprint {
				t.Errorf("parsed fingerprint = %q, want %q", md.Fingerprint, k.Fingerprint)
			}

			if md.KeyID != k.KeyID() {
				t.Errorf("parsed key ID = %q, want %q", md.KeyID, k.KeyID())
			}

			if md.Name != k.Name || md.Email != k.Email {
				t.Errorf("parsed identity = %q <%s>, want %q <%s>", md.Name, md.Email, k.Name, k.Email)
			}
		})
	}

	// The private armor must carry the secret half. ReadMetadata reads only
	// the public material, so it cannot tell the two blocks apart; the signer
	// constructor refuses a public-only armor by name.
	signer, err := openpgp.NewSignerFromArmor([]byte(k.ArmoredPrivateKey()), "")
	if err != nil {
		t.Fatalf("the private armor is not usable for signing: %v", err)
	}

	if got := signer.Fingerprint(); got != k.Fingerprint {
		t.Errorf("signer fingerprint = %q, want %q", got, k.Fingerprint)
	}

	if _, err := openpgp.NewSignerFromArmor([]byte(k.ArmoredPublicKey()), ""); err == nil {
		t.Error("the public armor was accepted as signing material, so the two exports are not distinct")
	}
}

// TestKeys_AreIsolated_AcrossKeyrings is the isolation claim. Distinct
// fingerprints and distinct directory paths are both true of two keys sharing
// one keyring; what matters for a fixture that runs beside other tests is that
// neither key can be found through the other's GNUPGHOME.
func TestKeys_AreIsolated_AcrossKeyrings(t *testing.T) {
	a := gpgkey.New(t)
	b := gpgkey.New(t)

	for _, pair := range []struct {
		home    string
		present string
		absent  string
	}{
		{home: a.GNUPGHOME, present: a.Fingerprint, absent: b.Fingerprint},
		{home: b.GNUPGHOME, present: b.Fingerprint, absent: a.Fingerprint},
	} {
		listed := listSecretFingerprints(t, pair.home)
		if !slices.Contains(listed, pair.present) {
			t.Errorf("%s does not hold its own key %s: %v", pair.home, pair.present, listed)
		}

		if slices.Contains(listed, pair.absent) {
			t.Errorf("%s can see the other keyring's key %s: %v", pair.home, pair.absent, listed)
		}
	}
}

// listSecretFingerprints reads the secret-key fingerprints in a GNUPGHOME
// directly, rather than through the fixture, so the fixture cannot be the
// source of its own isolation evidence.
func listSecretFingerprints(t *testing.T, homedir string) []string {
	t.Helper()

	//nolint:gosec // test infra; homedir is t.TempDir()-based.
	out, err := exec.CommandContext(t.Context(), "gpg", "--homedir", homedir,
		"--batch", "--with-colons", "--list-secret-keys").Output()
	if err != nil {
		t.Fatalf("list-secret-keys in %q: %v", homedir, err)
	}

	var fingerprints []string

	for line := range strings.SplitSeq(string(out), "\n") {
		if fields := strings.Split(line, ":"); strings.HasPrefix(line, "fpr:") && len(fields) >= 10 {
			fingerprints = append(fingerprints, fields[9])
		}
	}

	return fingerprints
}
