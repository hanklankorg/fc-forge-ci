// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package archguard

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/cargo"
	adaptergit "github.com/diggsweden/forge-ci/internal/adapters/git"
	"github.com/diggsweden/forge-ci/internal/adapters/xcode"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/stretchr/testify/require"
)

func TestExecBoundary_MissingProgramsAreAMissingPrerequisite(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	repo := &adaptergit.Repo{Dir: t.TempDir(), GitBin: missing}
	for _, call := range []func(context.Context) error{
		func(ctx context.Context) error {
			_, err := (&cargo.Adapter{Bin: missing}).Version(ctx)

			return err
		},
		func(ctx context.Context) error {
			_, err := (&xcode.Security{Bin: missing}).Run(ctx, "find-identity")

			return err
		},
		func(ctx context.Context) error {
			_, err := repo.HasStagedChanges(ctx)

			return err
		},
		func(ctx context.Context) error {
			_, err := repo.IsAncestor(ctx, "a", "b")

			return err
		},
	} {
		err := call(t.Context())
		require.ErrorIs(t, err, errs.ErrPrerequisite)
		require.NotErrorIs(t, err, errs.ErrDependencyUnavailable)
		require.EqualValues(t, 78, errs.ExitCodeFromError(err))
	}
}
