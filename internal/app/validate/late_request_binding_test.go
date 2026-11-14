// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/stretchr/testify/require"
)

type lateRequestGit struct {
	publicationGit

	requestObject string
	requestCommit string
	finalCommit   string
	tags          []string
	deleted       bool
	parent        string
}

func (g *lateRequestGit) RemoteTagObjectAtURL(_ context.Context, _, tag string, _ runcontext.Credential) (string, error) {
	if strings.HasPrefix(tag, "release-request/") {
		if g.deleted {
			return "", errs.ErrValidation
		}

		return g.requestObject, nil
	}

	return g.refs["refs/tags/v1.2.3"], nil
}

func (g *lateRequestGit) RemoteTagCommit(_ context.Context, _, tag string, _ runcontext.Credential) (string, error) {
	if strings.HasPrefix(tag, "release-request/") {
		return g.requestCommit, nil
	}

	return g.finalCommit, nil
}

func (g *lateRequestGit) RemoteVersionTags(context.Context, string, runcontext.Credential) ([]string, error) {
	return g.tags, nil
}

func (g *lateRequestGit) CommitParents(context.Context, string) ([]string, error) {
	return []string{g.parent}, nil
}

func (g *lateRequestGit) ChangedPaths(context.Context, string, string) ([]string, error) {
	return []string{"CHANGELOG.md"}, nil
}

func (g *lateRequestGit) PathsDiff(context.Context, string, string, []string) (string, error) {
	return "", nil
}

// FileAt finds a regular CHANGELOG.md at every commit, the one file the
// prepared release changes, and nothing else.
func (g *lateRequestGit) FileAt(_ context.Context, _, path string) (string, string, bool, error) {
	if path != "CHANGELOG.md" {
		return "", "", false, nil
	}

	return "", "100644", true, nil
}

// Compose the actual native policy and source validators with inert Git and
// signature-verification ports. No keys are generated, imported or used. The
// workflow guard separately requires this command sequence at both boundaries.
func TestLateNativeValidatorsRefuseRevocationBeforePrivateEffects(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"valid-child", "exact-P-retry", "no-bump", "request-moved", "request-deleted", "annotation-replaced", "unsigned-same-Q", "not-allowlisted", "final-moved", "final-superseded"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			requestSHA, releaseSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
			if scenario == "no-bump" {
				releaseSHA = requestSHA
			}

			requestObject := strings.Repeat("c", 40)
			repo := &lateRequestGit{body: sshSignedTagBody, refs: map[string]string{
				"HEAD": releaseSHA, "refs/tags/v1.2.3": strings.Repeat("d", 40),
				"refs/tags/release-request/v1.2.3": requestObject, "refs/tags/release-request/v1.2.3^{commit}": requestSHA,
			}, requestObject: requestObject, requestCommit: requestSHA, finalCommit: releaseSHA, parent: requestSHA, tags: []string{"v1.2.3"}}
			repo.sshVerify = func(string, string) (bool, string, error) { return true, "", nil }
			allowlist := filepath.Join(t.TempDir(), "allowed_signers")
			require.NoError(t, os.WriteFile(allowlist, []byte("inert port fixture\n"), 0o600))

			verify := func() error {
				for _, tag := range []string{"release-request/v1.2.3", "v1.2.3"} {
					if err := appvalidate.TagSignature(t.Context(), repo, io.Discard, output.Annotator{}, appvalidate.TagSignatureInput{
						Tag: tag, AllowedSignersPath: allowlist, RequireAllowlistedSigner: true, RequireValidSignature: true,
					}); err != nil {
						return err
					}
				}

				return apprelease.VerifyReleaseTag(t.Context(), repo, io.Discard, apprelease.VerifyReleaseTagInput{
					ReleaseSHA: releaseSHA, Tag: "v1.2.3", ExpectedParent: requestSHA,
					RequestIsRelease: scenario == "no-bump", ChangelogOnly: scenario != "no-bump",
				})
			}
			require.NoError(t, verify(), "prerequisite observation succeeds before the remote changes")

			switch scenario {
			case "request-moved":
				repo.requestCommit = releaseSHA
			case "request-deleted":
				repo.deleted = true
			case "annotation-replaced":
				repo.requestObject = strings.Repeat("e", 40)
			case "unsigned-same-Q":
				// Model a fresh checkout of the replacement: equality alone
				// would pass, but the real signature-policy owner must refuse.
				repo.requestObject = strings.Repeat("e", 40)
				repo.refs["refs/tags/release-request/v1.2.3"] = repo.requestObject
				repo.body = "object " + requestSHA + "\ntype commit\n"
			case "not-allowlisted":
				repo.sshVerify = func(string, string) (bool, string, error) { return false, "", errs.ErrPermissionDenied }
			case "final-moved":
				repo.finalCommit = requestSHA
			case "final-superseded":
				repo.tags = append(repo.tags, "v1.2.4")
			}

			for range 2 { // Signing boundary, then an exact-byte publishing retry.
				keySetup, sign, publish := 0, 0, 0

				err := verify()
				if err == nil {
					keySetup++
					sign++
					publish++
				}

				if scenario == "valid-child" || scenario == "exact-P-retry" || scenario == "no-bump" {
					require.NoError(t, err)
					require.Equal(t, 1, publish)
				} else {
					require.Error(t, err)
					require.Zero(t, keySetup)
					require.Zero(t, sign)
					require.Zero(t, publish)
				}
			}
		})
	}
}
