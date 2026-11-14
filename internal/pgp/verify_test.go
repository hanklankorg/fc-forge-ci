// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pgp_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/pgp"
)

func newEntity(t *testing.T, name string) *openpgp.Entity {
	t.Helper()

	entity, err := openpgp.NewEntity(name, "", strings.ToLower(name)+"@example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}

	return entity
}

func armoredPublic(t *testing.T, entity *openpgp.Entity) string {
	t.Helper()

	var buf bytes.Buffer

	writer, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := entity.Serialize(writer); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

// VerifyDetachedSigner reports the key that made a signature, from any block
// of a bundle: the signer may be the appended key of a rotation, and text
// between blocks is ignored. A bundle without the signer's key is a refusal,
// not a parse failure.
func TestVerifyDetachedSigner_FindsTheSignerInABundle(t *testing.T) {
	t.Parallel()

	signer := newEntity(t, "Signer")
	other := newEntity(t, "Other")
	message := "release payload\n"

	var signature bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&signature, signer, strings.NewReader(message), nil); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, bundle string
		signs        bool
	}{
		{"keyring without the signer", armoredPublic(t, other), false},
		{"signer appended after another key", armoredPublic(t, other) + armoredPublic(t, signer), true},
		{"junk between blocks", armoredPublic(t, other) + "not a key\n" + armoredPublic(t, signer), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			entity, err := pgp.VerifyDetachedSigner(strings.NewReader(message), bytes.NewReader(signature.Bytes()), []byte(test.bundle))
			if !test.signs {
				if !errors.Is(err, errs.ErrPermissionDenied) || entity != nil {
					t.Fatalf("got (%v, %v), want a refusal", entity, err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(entity.PrimaryKey.Fingerprint, signer.PrimaryKey.Fingerprint) {
				t.Fatalf("signer fingerprint %X, want %X", entity.PrimaryKey.Fingerprint, signer.PrimaryKey.Fingerprint)
			}
		})
	}
}
