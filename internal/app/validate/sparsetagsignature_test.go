//go:build integration

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"bytes"
	"strings"
	"testing"

	gocrypto "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/stretchr/testify/require"

	adaptergit "github.com/diggsweden/forge-ci/internal/adapters/git"
	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/testutil/gpgkey"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

// The late GitHub release boundary verifies the final tag in a sparse
// metadata checkout (`platform checkout --sparse .forge-ci`). git's
// sparse-checkout enables extensions.worktreeConfig, which go-git refuses to
// open, and the tag signature check used go-git: a correctly signed final tag
// was refused with exit 77. These tests run on the checkout the engine makes,
// with a tag that gpg itself signed.

// signedRelease is a repository whose v0.1.0 tag gpg itself signed with a
// throwaway key, on the commit that adds .forge-ci.
type signedRelease struct {
	source  *isolatedgit.Repo
	initial string
	release string
	key     *gpgkey.Key
}

func newSignedRelease(t *testing.T) signedRelease {
	t.Helper()

	key := gpgkey.New(t)
	source := isolatedgit.NewRepo(t)
	initial := source.HeadSHA()
	release := source.AddFile(".forge-ci/allowed_signers", "\n", "add trust metadata")
	source.Git("-c", "gpg.format=openpgp", "-c", "user.signingkey="+key.Fingerprint,
		"tag", "-s", "-m", "Release v0.1.0", "v0.1.0")

	return signedRelease{source: source, initial: initial, release: release, key: key}
}

// checkout makes a metadata checkout of the release commit with the adapter
// calls `platform checkout --fetch-all-refs` makes, in the same order; with
// sparse, those of `--sparse .forge-ci` too.
func (r signedRelease) checkout(t *testing.T, sparse bool) *adaptergit.Repo {
	t.Helper()

	ctx := t.Context()
	remote := "file://" + r.source.Dir
	checkout := &adaptergit.Repo{Dir: t.TempDir()}

	var anonymous runcontext.Credential

	require.NoError(t, checkout.InitWithObjectFormat(ctx, "sha1"))
	require.NoError(t, checkout.RemoteAdd(ctx, "origin", remote))

	if sparse {
		require.NoError(t, checkout.SparseInit(ctx, true))
		require.NoError(t, checkout.SparseSet(ctx, []string{".forge-ci"}))
	}

	require.NoError(t, checkout.FetchAllRefs(ctx, remote, anonymous))
	require.NoError(t, checkout.Fetch(ctx, remote, []string{r.release}, anonymous, 0))
	require.NoError(t, checkout.CheckoutDetach(ctx, "FETCH_HEAD"))

	if sparse {
		// The premise: without the extension this is any checkout, and
		// the tests would pass for the wrong reason.
		extension, err := checkout.Run(ctx, "config", "--get", "extensions.worktreeConfig")
		require.NoError(t, err)
		require.Equal(t, "true", extension, "git sparse-checkout no longer enables worktreeConfig; the fixture no longer reproduces the live checkout")
	}

	return checkout
}

// sparseSignedRelease returns the sparse metadata checkout of a signed
// release, and the key that signed it.
func sparseSignedRelease(t *testing.T) (*adaptergit.Repo, *gpgkey.Key) {
	t.Helper()

	release := newSignedRelease(t)

	return release.checkout(t, true), release.key
}

func otherPublicKey(t *testing.T) []byte {
	t.Helper()

	entity, err := gocrypto.NewEntity("Someone Else", "", "else@example.invalid", nil)
	require.NoError(t, err)

	var buf bytes.Buffer

	writer, err := armor.Encode(&buf, gocrypto.PublicKeyType, nil)
	require.NoError(t, err)
	require.NoError(t, entity.Serialize(writer))
	require.NoError(t, writer.Close())

	return buf.Bytes()
}

func TestVerifyTagSignature_ReadsASparseCheckout(t *testing.T) {
	checkout, key := sparseSignedRelease(t)

	signer, fingerprint, ok, err := checkout.VerifyTagSignature(t.Context(), "v0.1.0", []byte(key.ArmoredPublicKey()))
	require.NoError(t, err)
	require.True(t, ok, "a tag signed by the key in the keyring did not verify")
	require.Equal(t, key.Fingerprint, fingerprint)
	require.Equal(t, key.UID, signer)

	signer, fingerprint, ok, err = checkout.VerifyTagSignature(t.Context(), "v0.1.0", otherPublicKey(t))
	require.NoError(t, err, "a signature from another key is ok=false, not an error")
	require.False(t, ok, "a tag signed by a key outside the keyring verified")
	require.Empty(t, signer)
	require.Empty(t, fingerprint)
}

// TestTagSignature_RequiredFinalTagOnASparseCheckout runs the late boundary's
// final-tag command: --require-valid-signature, no GPG allowlist, and the
// configured release public key.
func TestTagSignature_RequiredFinalTagOnASparseCheckout(t *testing.T) {
	checkout, key := sparseSignedRelease(t)

	finalTag := func(releaseKey []byte) (string, error) {
		var out bytes.Buffer

		err := appvalidate.TagSignature(t.Context(), checkout, &out, output.Annotator{}, appvalidate.TagSignatureInput{
			Tag:                   "v0.1.0",
			ReleaseGPGPublicKey:   releaseKey,
			RequireValidSignature: true,
			AllowedGPGKeysPath:    "/dev/null",
		})

		return out.String(), err
	}

	out, err := finalTag([]byte(key.ArmoredPublicKey()))
	require.NoError(t, err, out)
	require.Contains(t, out, "GPG signature verified (fingerprint "+key.Fingerprint+")")

	out, err = finalTag(otherPublicKey(t))
	require.ErrorIs(t, err, errs.ErrPermissionDenied, out)
	require.Contains(t, out, "did not verify against any available key")
	require.False(t, strings.Contains(out, "verification skipped"), out)
}

// Every other go-git reader in the adapter falls back to git when go-git
// cannot open the repository. This pins that the fallbacks give the answers
// the in-process readers give, so the release reads the same facts from the
// sparse checkout as from a full one.
func TestGitReaders_AgreeOnSparseAndFullCheckouts(t *testing.T) {
	release := newSignedRelease(t)
	full := release.checkout(t, false)
	sparse := release.checkout(t, true)
	ctx := t.Context()

	read := func(repo *adaptergit.Repo) []any {
		t.Helper()

		tagObject, err := repo.RevParse(ctx, "refs/tags/v0.1.0")
		require.NoError(t, err)
		tagCommit, err := repo.TagSHA(ctx, "v0.1.0")
		require.NoError(t, err)
		pointing, err := repo.TagsPointingAt(ctx, release.release)
		require.NoError(t, err)
		ancestor, err := repo.IsAncestor(ctx, release.initial, release.release)
		require.NoError(t, err)
		objectType, err := repo.CatFileType(ctx, "v0.1.0")
		require.NoError(t, err)
		body, err := repo.CatFileTag(ctx, "v0.1.0")
		require.NoError(t, err)
		tagger, err := repo.TaggerInfo(ctx, "v0.1.0")
		require.NoError(t, err)
		message, err := repo.TagMessage(ctx, "v0.1.0")
		require.NoError(t, err)

		return []any{tagObject, tagCommit, pointing, ancestor, objectType, body, tagger, message}
	}

	want := read(full)
	require.Equal(t, release.release, want[1])
	require.Equal(t, want, read(sparse))
}
