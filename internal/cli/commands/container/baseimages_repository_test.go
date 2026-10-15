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

// A base-image statement says its images were built from --repository, so the
// signer signs only below that repository's owner on the registry it logs in
// to. The caller chooses the repository name there and nothing else. Every
// ledger below is entirely inside the repository it is signed with: the
// repository rule is the only thing that can refuse it.
func TestBaseImagesSign_SignsOnlyBelowTheSourceOwnerOnTheLoginRegistry(t *testing.T) {
	for _, tc := range []struct {
		name string
		// The placeholders HOST and OTHER stand for the forge's registry and a
		// second registry; an empty expected repository leaves the flag off.
		source, expected, registry string
		// external passes --external-registry: the repository is on a
		// registry other than the forge's, named by the repository itself.
		external bool
		refused  string
	}{
		{name: "derived default", source: baseSlug},
		{name: "derived default named outright", source: baseSlug, expected: "HOST/owner/app-base"},
		{name: "same owner, another name", source: baseSlug, expected: "HOST/owner/release-bases"},
		{name: "same owner, a nested name", source: baseSlug, expected: "HOST/owner/bases/alpine"},
		{name: "owner in another case", source: "OWNER/App", expected: "HOST/owner/release-bases"},
		{name: "nested owner namespace", source: "Group/Team/App", expected: "HOST/group/team/bases"},
		{name: "registry named with --registry", source: baseSlug, expected: "OTHER/owner/app-base", registry: "OTHER"},
		{name: "another owner", source: baseSlug, expected: "HOST/other/app-base", refused: "must be below the source repository's owner"},
		{name: "an owner that only starts the same", source: baseSlug, expected: "HOST/owner-two/app-base", refused: "must be below the source repository's owner"},
		{name: "the owner itself", source: baseSlug, expected: "HOST/owner", refused: "must be below the source repository's owner"},
		{name: "part of a nested owner namespace", source: "Group/Team/App", expected: "HOST/group/bases", refused: "must be below the source repository's owner"},
		{name: "another registry", source: baseSlug, expected: "OTHER/owner/app-base", refused: "must be on the registry this command logs in to"},
		{name: "the forge beside another --registry", source: baseSlug, expected: "HOST/owner/app-base", registry: "OTHER", refused: "must be on the registry this command logs in to"},
		{name: "the same host on another port", source: baseSlug, expected: "HOST0/owner/app-base", refused: "must be on the registry this command logs in to"},
		{name: "external registry named by its repository", source: baseSlug, expected: "OTHER/owner/app-base", external: true},
		{name: "external registry under another owner", source: baseSlug, expected: "OTHER/another-org/nanolinter-base", external: true},
		{name: "external repository beside another --registry", source: baseSlug, expected: "OTHER/another-org/app-base", registry: "HOST", external: true, refused: "must be on the registry this command logs in to"},
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

			if tc.external {
				args = append(args, "--external-registry")
			}

			fixture.writeLedger(t, repository)

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

			require.ErrorIs(t, signErr, errs.ErrValidation)
			require.ErrorContains(t, signErr, tc.refused)
			require.Empty(t, calls, "cosign ran for a refused repository")

			scratch, err := os.ReadDir(fixture.env.Path("runner-temp"))
			require.NoError(t, err)
			require.Empty(t, scratch, "a refusal left registry auth scratch behind")
		})
	}
}

// Promotion writes the final tags of the same repository, so it holds an
// explicit repository to the rule the signer does. An admitted repository
// goes on to the trusted key, which this run does not have.
func TestBaseImagesPromote_PromotesOnlyBelowTheSourceOwnerOnTheLoginRegistry(t *testing.T) {
	for _, tc := range []struct {
		name, expected string
		want           error
		contains       string
	}{
		{name: "derived default", want: errs.ErrMissingInput, contains: "Cosign public key is missing"},
		{name: "same owner, another name", expected: "forge.example:8443/owner/release-bases", want: errs.ErrMissingInput, contains: "Cosign public key is missing"},
		{name: "another owner", expected: "forge.example:8443/other/app-base", want: errs.ErrValidation, contains: "must be below the source repository's owner"},
		{name: "another registry", expected: "registry.example/owner/app-base", want: errs.ErrValidation, contains: "must be on the registry this command logs in to"},
		{name: "the forge without its port", expected: "forge.example/owner/app-base", want: errs.ErrValidation, contains: "must be on the registry this command logs in to"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newBaseSignFixture(t)

			args := []string{
				"container", "base-images", "promote", "--all-images-json", "[]",
				"--server-url", "https://forge.example:8443", "--repository", baseSlug, "--repository-suffix", "-base",
				"--expected-workflow", baseWorkflow, "--cosign-public-key-path", "keys/absent.pub",
				"--cosign-public-key-sha256", strings.Repeat("a", 64), "--registry-username", "fixture",
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
