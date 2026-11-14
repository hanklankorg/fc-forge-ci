// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/lender"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

var errLenderMustNotBeBuilt = errors.New("the lender must not be built")

type recordedLender struct{ requests []lender.Request }

func (l *recordedLender) Request(_ context.Context, req lender.Request) error {
	l.requests = append(l.requests, req)

	return nil
}

func TestRequestRunnerCommand_BuildsTheLenderFromFlagsAndTheTokenFile(t *testing.T) {
	t.Parallel()

	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("lender-canary\n"), 0o600))

	lend := &recordedLender{}

	var gotSelection lenderSelection

	var gotToken runcontext.Credential

	command := requestRunnerCommand(func(selection lenderSelection, token runcontext.Credential) (lender.Lender, string, error) {
		gotSelection, gotToken = selection, token

		return lend, "github:acme/lender", nil
	})

	err := command.Run(t.Context(), []string{"request-runner", "--lender", "GitHub", "--lender-repository", "acme/lender", "--arch", "arm64", "--ttl-minutes", "30", "--token-file", tokenFile})
	require.NoError(t, err)

	require.Equal(t, lenderSelection{Provider: "github", Target: "acme/lender", Ref: "main"}, gotSelection)
	require.Equal(t, "lender-canary", gotToken.For("https://api.github.com"), "an operator token file is unrestricted and trimmed")
	require.Len(t, lend.requests, 1)
	require.Equal(t, lender.ArchARM64, lend.requests[0].Arch)
}

// --lender none, or no lender at all, never reads a token and never builds a
// lender.
func TestRequestRunnerCommand_NoneRequestsNothing(t *testing.T) {
	t.Parallel()

	built := false

	command := requestRunnerCommand(func(lenderSelection, runcontext.Credential) (lender.Lender, string, error) {
		built = true

		return nil, "", errLenderMustNotBeBuilt
	})

	require.NoError(t, command.Run(t.Context(), []string{"request-runner", "--lender", "none", "--arch", "amd64"}))
	require.NoError(t, command.Run(t.Context(), []string{"request-runner", "--arch", "amd64"}), "no lender configured means none")
	require.False(t, built)
}

func TestNewLender_RefusesWhatItCannotBuild(t *testing.T) {
	t.Parallel()

	for name, selection := range map[string]lenderSelection{
		"unknown provider":  {Provider: "gitlab", Target: "acme/lender", Ref: "main"},
		"target not a repo": {Provider: "github", Target: "acme", Ref: "main"},
		"nested target":     {Provider: "github", Target: "acme/lender/extra", Ref: "main"},
		"empty ref":         {Provider: "github", Target: "acme/lender"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, _, err := newLender(selection, runcontext.OperatorCredential("x"))
			require.ErrorIs(t, err, errs.ErrUsage)
		})
	}

	lend, lenderName, err := newLender(lenderSelection{Provider: "github", Target: "acme/lender", Ref: "main"}, runcontext.OperatorCredential("x"))
	require.NoError(t, err)
	require.NotNil(t, lend)
	require.Equal(t, "github:acme/lender", lenderName)
}
