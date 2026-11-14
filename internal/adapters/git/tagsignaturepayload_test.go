//go:build integration

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	adaptergit "github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/gpgkey"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

// payloadFixture writes hand-built tag objects into a repository and signs
// them with real gpg, so VerifyTagSignature can be held against `git
// verify-tag` on objects git tag -s would never write.
type payloadFixture struct {
	t      *testing.T
	repo   *isolatedgit.Repo
	key    *gpgkey.Key
	header string
}

func newPayloadFixture(t *testing.T) payloadFixture {
	t.Helper()

	key := gpgkey.New(t)
	repo := isolatedgit.NewRepo(t)

	return payloadFixture{
		t: t, repo: repo, key: key,
		header: "object " + repo.HeadSHA() + "\ntype commit\ntag %s\ntagger Test Bot <bot@example.invalid> 1700000000 +0000\n",
	}
}

func (f payloadFixture) headers(name string) string {
	return strings.Replace(f.header, "%s", name, 1)
}

func (f payloadFixture) sign(payload string) string {
	f.t.Helper()

	//nolint:gosec // test infra; the keyring is the fixture's own.
	cmd := exec.CommandContext(f.t.Context(), "gpg", "--homedir", f.key.GNUPGHOME, "--batch", "--pinentry-mode", "loopback",
		"--passphrase", "", "--detach-sign", "--armor", "--local-user", f.key.Fingerprint)
	cmd.Stdin = strings.NewReader(payload)

	signature, err := cmd.Output()
	require.NoError(f.t, err)

	return string(signature)
}

// install writes body as a tag object, unchecked, under refs/tags/<name>.
func (f payloadFixture) install(name, body string) string {
	f.t.Helper()

	//nolint:gosec // test infra; the repository is the fixture's own.
	cmd := exec.CommandContext(f.t.Context(), "git", "hash-object", "-t", "tag", "-w", "--literally", "--stdin")
	cmd.Dir = f.repo.Dir
	cmd.Stdin = strings.NewReader(body)

	oid, err := cmd.Output()
	require.NoError(f.t, err)

	f.repo.Git("update-ref", "refs/tags/"+name, strings.TrimSpace(string(oid)))

	return strings.TrimSpace(string(oid))
}

func (f payloadFixture) gitVerifies(name string) bool {
	f.t.Helper()

	//nolint:gosec // test infra; the repository is the fixture's own.
	cmd := exec.CommandContext(f.t.Context(), "git", "verify-tag", name)
	cmd.Dir = f.repo.Dir

	return cmd.Run() == nil
}

func (f payloadFixture) verify(name string) (bool, error) {
	f.t.Helper()

	_, _, ok, err := (&adaptergit.Repo{Dir: f.repo.Dir}).VerifyTagSignature(f.t.Context(), name, []byte(f.key.ArmoredPublicKey()))

	return ok, err
}

// A tag in a repository using both hash algorithms carries a compatibility
// signature header that its own signature excludes; git strips it before
// verifying. Each case is held against git itself, both ways: a signature
// git accepts must verify, and one git refuses must not.
func TestVerifyTagSignature_StripsTheCompatibilityHeaderLikeGit(t *testing.T) {
	fixture := newPayloadFixture(t)
	header := func(name string) string {
		return name + " -----BEGIN PGP SIGNATURE-----\n \n AAAA\n -----END PGP SIGNATURE-----\n"
	}

	for _, test := range []struct {
		name, headers, signed string
		verifies              bool
	}{
		{"gpgsig-sha256, signed without it", header("gpgsig-sha256"), "", true},
		{"gpgsig-sha256, signed with it", header("gpgsig-sha256"), header("gpgsig-sha256"), false},
		{"gpgsig, signed without it", header("gpgsig"), "", true},
		{"gpgsig, signed with it", header("gpgsig"), header("gpgsig"), false},
		// git removes only the last such header.
		{"both, signed keeping the first", header("gpgsig") + header("gpgsig-sha256"), header("gpgsig"), true},
		{"both, signed without either", header("gpgsig") + header("gpgsig-sha256"), "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tag := strings.NewReplacer(" ", "-", ",", "").Replace(test.name)
			signed := fixture.headers(tag) + test.signed + "\nRelease\n"
			object := fixture.headers(tag) + test.headers + "\nRelease\n"
			fixture.install(tag, object+fixture.sign(signed))

			require.Equal(t, test.verifies, fixture.gitVerifies(tag), "the case no longer states what git does")

			ok, err := fixture.verify(tag)
			require.NoError(t, err)
			require.Equal(t, test.verifies, ok)
		})
	}
}

// git verify-tag accepts a signed tag object under any ref name. The release
// verifies a name, so a ref that points at another release's signed object
// is refused, not reported as that release's signature.
func TestVerifyTagSignature_RefusesARefNamingAnotherTag(t *testing.T) {
	fixture := newPayloadFixture(t)
	payload := fixture.headers("v0.1.0") + "\nRelease v0.1.0\n"
	oid := fixture.install("v0.1.0", payload+fixture.sign(payload))
	fixture.repo.Git("update-ref", "refs/tags/v9.9.9", oid)

	ok, err := fixture.verify("v0.1.0")
	require.NoError(t, err)
	require.True(t, ok)

	require.True(t, fixture.gitVerifies("v9.9.9"), "git itself does not bind the name")

	ok, err = fixture.verify("v9.9.9")
	require.ErrorIs(t, err, errs.ErrValidation)
	require.ErrorContains(t, err, `named "v0.1.0"`)
	require.False(t, ok)

	ok, err = fixture.verify("refs/tags/v0.1.0")
	require.NoError(t, err, "the full ref spelling names the same tag")
	require.True(t, ok)
}
