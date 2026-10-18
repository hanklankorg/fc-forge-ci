// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"bytes"
	"context"
	"fmt"
	appbuild "github.com/diggsweden/forge-ci/internal/app/build"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/stretchr/testify/require"
	"io"
	"os"
	"strings"
	"testing"
)

type npmDependencyProbe struct {
	fakeNPMRunner
	t         *testing.T
	config    string
	authCalls int
	failure   error
}

func (p *npmDependencyProbe) RunWithEnv(ctx context.Context, dir string, env []string, out, stderr io.Writer, args ...string) error {
	p.authCalls++
	require.Equal(p.t, []string{"ci"}, args)
	require.Contains(p.t, env, "DEPENDENCY_TOKEN=synthetic-private-token")

	for _, value := range env {
		if path, ok := strings.CutPrefix(value, "NPM_CONFIG_USERCONFIG="); ok {
			p.config = path
		}
	}

	info, err := os.Stat(p.config)
	require.NoError(p.t, err)
	require.Equal(p.t, os.FileMode(0o600), info.Mode().Perm())
	body, err := os.ReadFile(p.config)
	require.NoError(p.t, err)
	require.Equal(p.t, "//registry.example/private/:_authToken=${DEPENDENCY_TOKEN}\n", string(body))

	_, _ = io.WriteString(out, "diagnostic synthetic-private-")
	_, _ = io.WriteString(out, "token\n")

	if p.failure != nil {
		return p.failure
	}

	return p.fakeNPMRunner.RunInherit(ctx, dir, out, stderr, args...)
}

func (p *npmDependencyProbe) RunInherit(ctx context.Context, dir string, out, stderr io.Writer, args ...string) error {
	_, err := os.Stat(p.config)
	require.ErrorIs(p.t, err, os.ErrNotExist, "authentication config must be removed before later build phases")

	return p.fakeNPMRunner.RunInherit(ctx, dir, out, stderr, args...)
}

func TestNPMDependencyCredentialsArePhaseScoped(t *testing.T) {
	t.Parallel()

	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			t.Parallel()

			probe := &npmDependencyProbe{t: t}
			if fail {
				probe.failure = fmt.Errorf("synthetic-private-token rejected: %w", errs.ErrPermissionDenied)
			}

			var out bytes.Buffer

			err := appbuild.NPMReleaseBuild(t.Context(), &recordingSummarySink{}, probe, &fakeNPMRunner{}, output.Annotator{}, &out, &out, appbuild.NPMReleaseBuildInput{
				ReleaseBuildOptions: appbuild.ReleaseBuildOptions{Dir: newNPMDir(t)},
				DependencyAccess:    &appbuild.NPMDependencyAccess{Registry: "https://registry.example/private", Token: "synthetic-private-token", Environment: []string{"PATH=/fixture"}},
			})
			if fail {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "synthetic-private-token")
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, 1, probe.authCalls)
			require.NotContains(t, out.String(), "synthetic-private-token")

			_, statErr := os.Stat(probe.config)
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}
