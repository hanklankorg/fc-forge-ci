// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/stretchr/testify/require"
)

func publicationFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "org", "example", "demo", "1.2.3")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	pom := `<project><modelVersion>4.0.0</modelVersion><groupId>org.example</groupId><artifactId>demo</artifactId><version>1.2.3</version><name>Demo</name><description>Fixture</description><url>https://example.invalid</url><licenses><license><name>CC0</name><url>https://example.invalid/license</url></license></licenses><developers><developer><id>fixture</id></developer></developers><scm><url>https://example.invalid/source</url></scm></project>`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "demo-1.2.3.pom"), []byte(pom), 0o600))

	for _, suffix := range []string{".jar", "-sources.jar", "-javadoc.jar"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "demo-1.2.3"+suffix), []byte("fixture bytes "+suffix), 0o600))
	}

	return root
}

func TestMavenPublicationRefusesIncompleteOrUnconfinedSets(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"complete", "missing sources", "wrong version", "symlink", "unexpected file", "empty classifier", "noncanonical classifier"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := publicationFixture(t)
			version := "1.2.3"

			switch name {
			case "missing sources":
				require.NoError(t, os.Remove(filepath.Join(dir, "org/example/demo/1.2.3/demo-1.2.3-sources.jar")))
			case "wrong version":
				version = "1.2.4"
			case "symlink":
				require.NoError(t, os.Symlink("/outside", filepath.Join(dir, "linked.jar")))
			case "unexpected file":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "credentials.txt"), []byte("fixture"), 0o600))
			case "empty classifier":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "org/example/demo/1.2.3/demo-1.2.3-.jar"), []byte("would alias the main artifact"), 0o600))
			case "noncanonical classifier":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "org/example/demo/1.2.3/demo-1.2.3.sources.jar"), []byte("classifier separator must be a hyphen"), 0o600))
			}

			set, err := apppublish.InspectMavenPublication(dir, version, true)
			if name == "complete" {
				require.NoError(t, err)
				require.Len(t, set.Payloads, 4)
			} else {
				require.Error(t, err)
			}
		})
	}
}

type publicationSigner struct{ entity *openpgp.Entity }

func (s publicationSigner) DetachedSignArmored(_ context.Context, _, _, input, output string) error {
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	signature, err := os.Create(output)
	if err != nil {
		return err
	}

	defer func() { _ = signature.Close() }()

	return openpgp.ArmoredDetachSign(signature, s.entity, file, nil)
}

func TestMavenPublicationSignaturesBindEveryPayload(t *testing.T) {
	t.Parallel()
	dir := publicationFixture(t)
	key, err := openpgp.NewEntity("Fixture", "", "fixture@example.invalid", nil)
	require.NoError(t, err)

	var public bytes.Buffer

	encoded, err := armor.Encode(&public, openpgp.PublicKeyType, nil)
	require.NoError(t, err)
	require.NoError(t, key.Serialize(encoded))
	require.NoError(t, encoded.Close())
	require.NoError(t, apppublish.SignMavenPublication(t.Context(), publicationSigner{key}, io.Discard, apppublish.MavenSignInput{Directory: dir, Version: "1.2.3", Fingerprint: fmt.Sprintf("%X", key.PrimaryKey.Fingerprint), PublicKey: public.String()}))
	set, err := apppublish.VerifyMavenPublication(t.Context(), dir, "1.2.3", public.String())
	require.NoError(t, err)
	require.Len(t, set.Payloads, 4)

	checksum := filepath.Join(dir, "org/example/demo/1.2.3/demo-1.2.3.jar.sha256")
	original, err := os.ReadFile(checksum)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(checksum, []byte("tampered"), 0o600))
	_, err = apppublish.VerifyMavenPublication(t.Context(), dir, "1.2.3", public.String())
	require.ErrorContains(t, err, "checksum disagrees")
	require.NoError(t, os.WriteFile(checksum, original, 0o600)) // #nosec G703 -- restoring a fixture at a fixed path under t.TempDir.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "org/example/demo/1.2.3/demo-1.2.3.jar"), []byte("changed"), 0o600))
	_, err = apppublish.VerifyMavenPublication(t.Context(), dir, "1.2.3", public.String())
	require.Error(t, err)
}
