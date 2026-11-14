// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The signer signs in the one base-image repository it is given, or the one
// derived from --server-url and --repository, and logs in to that repository's
// registry. The repository may be under any owner and on any registry: the
// registry token decides what can be written there. Every ledger below is
// inside the repository it is signed with unless the case says otherwise.
func TestBaseImagesSign_SignsInTheGivenRepositoryOnItsRegistry(t *testing.T) {
	for _, tc := range []struct {
		name string
		// The placeholders HOST and OTHER stand for the forge's registry and a
		// second registry; an empty expected repository leaves the flag off.
		source, expected, registry string
		// ledger, when set, is the repository the ledger's entries are in.
		ledger  string
		refused string
	}{
		{name: "derived default", source: baseSlug},
		{name: "derived default named outright", source: baseSlug, expected: "HOST/owner/app-base"},
		{name: "same owner, another name", source: baseSlug, expected: "HOST/owner/release-bases"},
		{name: "another owner", source: baseSlug, expected: "HOST/other/app-base"},
		{name: "another registry", source: baseSlug, expected: "OTHER/another-org/app-base"},
		{name: "the forge's registry named with --registry", source: baseSlug, expected: "OTHER/owner/app-base", registry: "OTHER"},
		{name: "a ledger outside the repository", source: baseSlug, expected: "HOST/owner/app-base", ledger: "HOST/owner/elsewhere", refused: "must be under"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newBaseSignFixture(t)
			other := strings.TrimPrefix(newBaseSignRegistry(t).URL, "http://")
			place := strings.NewReplacer("HOST", fixture.host, "OTHER", other).Replace

			repository := fixture.host + "/" + strings.ToLower(tc.source) + "-base"
			args := []string{
				"container", "base-images", "sign", "--ledger", "base-images.ledger.json",
				"--server-url", fixture.serverURL, "--repository", tc.source, "--repository-suffix", "-base",
				"--workflow", baseWorkflow, "--source-sha", basePinnedCommit, "--started-on", baseStartedOn,
				"--sbom-path-pattern", baseSBOMPattern, "--method", "kms", "--key", "env://COSIGN_KEY",
				"--registry-username", "fixture",
			}

			if tc.expected != "" {
				repository = place(tc.expected)
				args = append(args, "--expected-repository", repository)
			}

			if tc.registry != "" {
				args = append(args, "--registry", place(tc.registry))
			}

			ledger := repository
			if tc.ledger != "" {
				ledger = place(tc.ledger)
			}

			fixture.writeLedger(t, ledger)

			var signErr error

			calls, _ := fixture.given(t, func() { signErr = fixture.runErr(t, args...) })

			if tc.refused == "" {
				require.NoError(t, signErr)
				require.Len(t, calls, 6, "two images, each signed and attested twice")

				for _, call := range calls {
					require.Contains(t, call[len(call)-1], repository+"@sha256:")
				}

				return
			}

			require.ErrorContains(t, signErr, tc.refused)
			require.Empty(t, calls, "cosign ran for a refused ledger")

			scratch, err := os.ReadDir(fixture.env.Path("runner-temp"))
			require.NoError(t, err)
			require.Empty(t, scratch, "a refusal left registry auth scratch behind")
		})
	}
}

// Promotion logs in to the base-image repository's own registry. An empty
// registry user is the source owner on the forge's registry; another registry
// needs its user named, refused before the key or a credential is read.
func TestBaseImagesPromote_NeedsARegistryUserOffTheForge(t *testing.T) {
	for _, tc := range []struct {
		name, expected, user string
		want                 error
		contains             string
	}{
		{name: "derived default", user: "fixture", want: errs.ErrMissingInput, contains: "Cosign public key is missing"},
		{name: "another owner", expected: "forge.example:8443/other/app-base", user: "fixture", want: errs.ErrMissingInput, contains: "Cosign public key is missing"},
		{name: "the forge's owner by default", want: errs.ErrMissingInput, contains: "Cosign public key is missing"},
		{name: "another registry with its user", expected: "registry.example/owner/app-base", user: "fixture", want: errs.ErrMissingInput, contains: "Cosign public key is missing"},
		{name: "another registry without a user", expected: "registry.example/owner/app-base", want: errs.ErrUsage, contains: "registry-user"},
		{name: "the forge without its port", expected: "forge.example/owner/app-base", want: errs.ErrUsage, contains: "registry-user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newBaseSignFixture(t)

			args := []string{
				"container", "base-images", "promote", "--all-images-json", "[]", "--base-inputs-json", "[]",
				"--server-url", "https://forge.example:8443", "--repository", baseSlug, "--repository-suffix", "-base",
				"--expected-workflow", baseWorkflow, "--cosign-public-key-path", "keys/absent.pub",
				"--cosign-public-key-sha256", strings.Repeat("a", 64),
			}
			if tc.user != "" {
				args = append(args, "--registry-username", tc.user)
			}

			if tc.expected != "" {
				args = append(args, "--expected-repository", tc.expected)
			}

			err := fixture.runErr(t, args...)
			require.ErrorIs(t, err, tc.want)
			require.ErrorContains(t, err, tc.contains)
		})
	}
}
