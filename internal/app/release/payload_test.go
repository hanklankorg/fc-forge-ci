// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"os"
	"path/filepath"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/stretchr/testify/require"
)

func TestCanonicalPayloadBindsBothRootsAndProducerLedgers(t *testing.T) {
	t.Parallel()

	for _, mutate := range []string{"none", "dist/base-images.json", "dist/release-images.json", "premade-sboms/base.cdx.json", "dist/image.cdx.json", "unexpected-root", "reserved", "link", "control", "root-link", "collision"} {
		t.Run(mutate, func(t *testing.T) {
			t.Parallel()

			source := t.TempDir()
			for _, path := range []string{"dist/base-images.json", "dist/release-images.json", "premade-sboms/base.cdx.json", "dist/image.cdx.json"} {
				full := filepath.Join(source, path)
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
				require.NoError(t, os.WriteFile(full, []byte("{}\n"), 0o600))
			}

			digest, err := apprelease.CanonicalPayloadDigest(source)
			require.NoError(t, err)
			legacyDigest, err := apprelease.DistDigestWithManifestRoot(source, ".")
			require.NoError(t, err)
			require.Equal(t, legacyDigest, digest, "reuse the existing hash wire contract")

			switch mutate {
			case "none":
			case "unexpected-root":
				require.NoError(t, os.Mkdir(filepath.Join(source, "surprise"), 0o700))
			case "reserved":
				require.NoError(t, os.WriteFile(filepath.Join(source, "dist/base-images-publication.json"), []byte("[]"), 0o600))
			case "link":
				require.NoError(t, os.Symlink("../dist/base-images.json", filepath.Join(source, "premade-sboms/link")))
			case "control":
				require.NoError(t, os.WriteFile(filepath.Join(source, "dist/late\n"), []byte("bad"), 0o600))
			case "root-link":
				link := filepath.Join(t.TempDir(), "payload")
				require.NoError(t, os.Symlink(source, link))
				source = link
			case "collision":
				require.NoError(t, os.Rename(filepath.Join(source, "premade-sboms"), filepath.Join(t.TempDir(), "saved")))
				require.NoError(t, os.WriteFile(filepath.Join(source, "premade-sboms"), []byte("file not directory"), 0o600))
			default:
				require.NoError(t, os.WriteFile(filepath.Join(source, mutate), []byte("changed"), 0o600))
			}

			destination := filepath.Join(t.TempDir(), "private-workspace")

			err = apprelease.StageVerifiedCanonicalPayload(t.Context(), source, destination, digest)
			if mutate == "none" {
				require.NoError(t, err)
				require.NoError(t, apprelease.VerifyCanonicalPayload(destination, digest))
				body, err := os.ReadFile(filepath.Join(destination, "premade-sboms/base.cdx.json"))
				require.NoError(t, err)
				require.Equal(t, "{}\n", string(body))
				require.Error(t, apprelease.StageVerifiedCanonicalPayload(t.Context(), source, destination, digest))
			} else {
				require.Error(t, err)
				require.NoDirExists(t, destination)
				require.Error(t, apprelease.VerifyCanonicalPayload(source, digest))
			}
		})
	}
}

// Filesystem hard links are regular files, not path redirects. The handoff copy
// deliberately breaks their identity; its digest binds the privately copied
// bytes. No link-count policy or same-user race model is introduced here.
func TestCanonicalPayloadCopyBreaksProducerHardLinkIdentity(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(source, "dist"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(source, "premade-sboms"), 0o700))
	outside := filepath.Join(t.TempDir(), "producer-bytes")
	require.NoError(t, os.WriteFile(outside, []byte("{\"producer\":true}"), 0o600))

	for _, name := range []string{"dist/evidence.json", "premade-sboms/base.json"} {
		require.NoError(t, os.Link(outside, filepath.Join(source, name)))
	}

	digest, err := apprelease.CanonicalPayloadDigest(source)
	require.NoError(t, err)
	destination := filepath.Join(t.TempDir(), "verified-root")
	require.NoError(t, apprelease.StageVerifiedCanonicalPayload(t.Context(), source, destination, digest))

	producerInfo, err := os.Stat(outside)
	require.NoError(t, err)
	first, err := os.Stat(filepath.Join(destination, "dist/evidence.json"))
	require.NoError(t, err)
	second, err := os.Stat(filepath.Join(destination, "premade-sboms/base.json"))
	require.NoError(t, err)
	require.False(t, os.SameFile(producerInfo, first))
	require.False(t, os.SameFile(first, second))
	require.NoError(t, os.WriteFile(outside, []byte("changed producer bytes"), 0o600))
	require.NoError(t, apprelease.VerifyCanonicalPayload(destination, digest))
}
