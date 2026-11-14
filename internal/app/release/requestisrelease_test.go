// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"context"
	"io"
	"strings"
	"testing"

	apprelease "github.com/diggsweden/forge-ci/internal/app/release"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/stretchr/testify/require"
)

func TestVerifyReleaseTagExplicitNoBumpStillBindsRequest(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"valid", "exact publication retry", "moved request", "replaced annotation", "missing request", "missing source", "child is not no-bump", "profile is not no-bump"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			var calls []tagVerifyCall

			git := fakeTagGit{head: sha, tagCommit: sha, request: sha, requestObject: strings.Repeat("c", 40)}
			in := apprelease.VerifyReleaseTagInput{ReleaseSHA: sha, Tag: "v1.2.3", ExpectedParent: sha, RequestIsRelease: true}
			git.observe = func(_ context.Context, call tagVerifyCall) error {
				calls = append(calls, call)
				if scenario == "missing request" && call.method == "RemoteTagObjectAtURL" && call.ref == "release-request/v1.2.3" {
					return errs.ErrValidation
				}

				return nil
			}

			switch scenario {
			case "moved request":
				git.remoteRequestCommit = strings.Repeat("d", 40)
			case "replaced annotation":
				git.remoteRequestObject = strings.Repeat("d", 40)
			case "missing source":
				in.ExpectedParent = ""
			case "child is not no-bump":
				in.ExpectedParent = strings.Repeat("b", 40)
			case "profile is not no-bump":
				in.ChangelogOnly = true
			}

			err := apprelease.VerifyReleaseTag(t.Context(), git, io.Discard, in)
			if scenario == "valid" || scenario == "exact publication retry" {
				require.NoError(t, err)
				require.Contains(t, calls, tagVerifyCall{method: "RemoteTagCommit", repository: "origin", ref: "release-request/v1.2.3"})
			} else {
				require.Error(t, err)
			}

			for _, call := range calls {
				require.NotEqual(t, "CommitParents", call.method)
			}
		})
	}
}
