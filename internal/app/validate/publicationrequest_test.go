// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/stretchr/testify/require"
)

type publicationGit struct {
	fakeTagGit
	refs map[string]string
}

func (g *publicationGit) RevParse(_ context.Context, ref string) (string, error) {
	if sha, ok := g.refs[ref]; ok {
		return sha, nil
	}

	return "", fmt.Errorf("unknown fixture ref: %w", errs.ErrValidation)
}

func TestPublicationRequestRechecksExactReleaseAndSigner(t *testing.T) {
	t.Parallel()
	key, fingerprint := armoredPublicKey(t)
	request, release := strings.Repeat("a", 40), strings.Repeat("b", 40)

	for _, name := range []string{"valid", "wrong checkout", "wrong final tag", "wrong parent", "unverified signer", "missing allowlist", "snapshot request", "moving ref"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".forge-ci"), 0o700))

			if name != "missing allowlist" {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".forge-ci", "allowed_gpg_keys.asc"), key, 0o600))
			}

			repo := &publicationGit{fakeTagGit: fakeTagGit{body: gpgSignedTagBody, verifySigOK: name != "unverified signer", verifySigFingerprint: fingerprint}, refs: map[string]string{
				"HEAD": release, "refs/tags/v1.2.3^{commit}": release,
				"refs/tags/release-request/v1.2.3^{commit}": request, "refs/tags/v1.2.3^{commit}^": request,
			}}
			in := appvalidate.PublicationRequestInput{RequestTag: "release-request/v1.2.3", ReleaseSHA: release, AuthorizationDir: root}

			switch name {
			case "wrong checkout":
				repo.refs["HEAD"] = request
			case "wrong final tag":
				repo.refs["refs/tags/v1.2.3^{commit}"] = request
			case "wrong parent":
				repo.refs["refs/tags/v1.2.3^{commit}^"] = release
			case "snapshot request":
				in.RequestTag = "release-request/v1.2.3-SNAPSHOT"
			case "moving ref":
				in.ReleaseSHA = "main"
			}

			err := appvalidate.PublicationRequest(t.Context(), repo, io.Discard, output.Annotator{}, in)
			if name == "valid" {
				require.NoError(t, err)
				require.Equal(t, 2, repo.verifySigCallCount)
			} else {
				require.Error(t, err)
			}
		})
	}
}
