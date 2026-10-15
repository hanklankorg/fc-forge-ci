// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
)

var errTagBoundary = errors.New("owned tag dependency failure")

func TestTagObjectBoundary_AnnotatedInBothModes(t *testing.T) { //nolint:gocognit // positive and negative object types independently cover signed and unsigned reruns.
	t.Parallel()

	for _, signed := range []bool{false, true} {
		for _, kind := range []string{"tag", "commit", "type failure"} {
			repo := &fakeTagReleaseRepo{exists: true, localSHA: headSHA, tagType: kind}
			if kind == "type failure" {
				repo.typeErr = errTagBoundary
			}

			if !signed {
				repo.signatureErr = errTagBoundary
			}

			sink := fakeoutputsink.New(t)

			var out bytes.Buffer

			_, err := appversion.TagRelease(t.Context(), repo, appversion.TagReleaseInput{Tag: "v3.5.7", Signed: signed}, sink, &out)
			if !slices.Equal(repo.typeRefs, []string{"refs/tags/v3.5.7"}) {
				t.Fatalf("type lookup=%v", repo.typeRefs)
			}

			if kind == "tag" { //nolint:nestif // the successful type has distinct signature expectations in each mode.
				if err != nil || repo.pushed != "v3.5.7" || repo.created != "" {
					t.Fatalf("annotated rerun err=%v repo=%+v", err, repo)
				}

				want := 0
				if signed {
					want = 1
				}

				if len(repo.verified) != want {
					t.Fatalf("signed=%v verified=%v", signed, repo.verified)
				}
			} else {
				cause := errs.ErrValidation
				if kind == "type failure" {
					cause = errTagBoundary
				}

				if !errors.Is(err, cause) || len(repo.verified) != 0 || repo.pushed != "" || repo.created != "" || len(sink.Keys()) != 0 || out.Len() != 0 {
					t.Fatalf("kind=%s err=%v repo=%+v", kind, err, repo)
				}
			}
		}
	}
}
