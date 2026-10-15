// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package publish_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apppublish "github.com/diggsweden/forge-ci/internal/app/publish"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/publish"
)

func TestRegistryAuth_OK(t *testing.T) {
	t.Parallel()

	var out, stderr bytes.Buffer

	err := apppublish.RegistryAuth(context.Background(), &out, &stderr, output.NewAnnotator(&stderr, output.FormatGitHub), publish.RegistryAuthInput{
		UseCIToken: true,
		Registry:   "ghcr.io",
	})
	require.NoError(t, err)
	require.Contains(t, out.String(), "✓ Registry authentication")
}

func TestRegistryAuth_ErrorWhenNoPasswordAndCustomAuth(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer

	err := apppublish.RegistryAuth(context.Background(), &bytes.Buffer{}, &stderr, output.NewAnnotator(&stderr, output.FormatGitHub), publish.RegistryAuthInput{
		UseCIToken:  false,
		Registry:    "ghcr.io",
		HasPassword: false,
	})
	// The class decides the exit code. validate-release-prerequisites
	// documents this path as failing closed with EX_NOPERM (77), which
	// ErrPermissionDenied is what produces; require.Error accepted any
	// failure, including one from a later stage for another reason.
	require.ErrorIs(t, err, errs.ErrPermissionDenied)
	require.Contains(t, stderr.String(), "::error::registry-password")
}

func TestRegistryAuth_WarningsOnCITokenWithCustomRegistry(t *testing.T) {
	t.Parallel()

	var out, stderr bytes.Buffer

	err := apppublish.RegistryAuth(context.Background(), &out, &stderr, output.NewAnnotator(&stderr, output.FormatGitHub), publish.RegistryAuthInput{
		UseCIToken:       true,
		Registry:         "https://npm.pkg.github.com",
		ExpectedRegistry: "ghcr.io",
	})
	require.NoError(t, err)
	require.Contains(t, stderr.String(), "::warning::Using CI token")
	require.Contains(t, out.String(), "✓ Registry authentication")
}
