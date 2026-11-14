// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeprovider"
	"github.com/stretchr/testify/require"
)

func TestPrerequisitesGPGPublicAuthorizationRefusesWithoutPrivateProcesses(t *testing.T) {
	for _, refusal := range []string{"signature", "foreign-signer"} {
		t.Run(refusal, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			require.NoError(t, os.Mkdir(".forge-ci", 0o700))
			// Existing throwaway PUBLIC fixture only: no private key or crypto
			// acceptance claim. Native verification receives public material.
			require.NoError(t, os.WriteFile(".forge-ci/allowed_gpg_keys.asc", []byte(realArmorBlock), 0o600))
			bin := t.TempDir()
			t.Setenv("PATH", bin)
			t.Setenv("GPG_PRIVATE_KEY", "must-not-be-resolved")
			t.Setenv("GPG_PASSPHRASE", "must-not-be-resolved")

			for _, name := range []string{"gpg", "gpgconf", "git"} {
				require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf process >> private-processes\nexit 97\n"), 0o700))
			}

			var (
				publicVerifications          atomic.Int32
				verifiedTag, verificationKey string
			)

			const request = "release-request/v1.0.0"

			result, err := appvalidate.Prerequisites(t.Context(), appvalidate.PrerequisitesDeps{
				GitRepo: stubGit{
					tags: []string{request}, tagSHA: "abc1234", verifyOK: refusal == "foreign-signer",
					verifyCall: func(_ context.Context, tag string, key []byte) {
						publicVerifications.Add(1)

						verifiedTag, verificationKey = tag, string(key)
					},
				},
				Provider: fakeprovider.New(t).WithPlatform(provider.ForgeGitHub).WithBotPermissions(provider.BotPermissions{
					UserAccessible: true, RepoAccessible: true, BranchesAccessible: true,
				}),
			}, io.Discard, output.Annotator{}, appvalidate.PrerequisitesInput{
				RefType: "tag", Tag: request, Ref: "refs/tags/" + request, Branch: "main",
				ReleaseToken: "github_pat_inert", Repository: "owner/repo", ReleaseGPGPublicKey: realArmorBlock,
				RequireAllowlistedSigner: true, RequiresGPGSigning: true, SignArtifacts: true,
			})
			require.ErrorIs(t, err, errs.ErrPermissionDenied)
			require.EqualValues(t, 1, publicVerifications.Load(), "public signature verification must actually execute")
			require.Equal(t, request, verifiedTag)
			require.Equal(t, strings.TrimSpace(realArmorBlock+realArmorBlock), verificationKey, "allowlist and configured public keys remain verification material")
			require.NoFileExists(t, filepath.Join(root, "private-processes"))

			var signature, publicKey bool

			for _, check := range result.Checks {
				switch check.Name {
				case "tag-signature":
					signature = true

					require.False(t, check.Skipped)
					require.ErrorIs(t, check.Err, errs.ErrPermissionDenied)
				case "gpg-public-key":
					publicKey = true

					require.False(t, check.Skipped)
					require.NoError(t, check.Err)
				}
			}

			require.True(t, signature)
			require.True(t, publicKey)
		})
	}
}
