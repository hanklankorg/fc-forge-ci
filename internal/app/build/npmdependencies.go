// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	domainbuild "github.com/diggsweden/forge-ci/internal/domain/build"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/secrettext"
)

// NPMDependencyAccess is invocation-only authentication, never plan/config data.
// Environment is the credential-free base; Token is added only for npm ci.
type NPMDependencyAccess struct {
	Registry    string
	Token       string
	Environment []string
}

type npmEnvironmentRunner interface {
	RunWithEnv(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, args ...string) error
}

func installNPMDependencies(ctx context.Context, runner NPMRunner, out, stderr io.Writer, dir string, access *NPMDependencyAccess) (result error) {
	if access == nil {
		return runner.RunInherit(ctx, dir, out, stderr, "ci")
	}

	body, err := domainbuild.NPMDependencyAuthConfig(access.Registry)
	if err != nil {
		return err
	}

	if access.Token == "" {
		return fmt.Errorf("private dependencies require DEPENDENCY_TOKEN or --dependency-token-file: %w", errs.ErrPermissionDenied)
	}

	ops, ok := runner.(npmEnvironmentRunner)
	if !ok {
		return fmt.Errorf("npm runner cannot scope dependency credentials: %w", errs.ErrUsage)
	}

	file, err := os.CreateTemp("", "forge-ci-npm-dependencies-*")
	if err != nil {
		return err
	}

	defer func() { result = errors.Join(result, os.Remove(file.Name())) }()

	_, writeErr := io.WriteString(file, body)
	if closeErr := errors.Join(writeErr, file.Close()); closeErr != nil {
		return closeErr
	}

	env := append(append([]string{}, access.Environment...), "DEPENDENCY_TOKEN="+access.Token, "NPM_CONFIG_USERCONFIG="+file.Name())
	stdoutSafe, stderrSafe := secrettext.NewWriter(out, access.Token), secrettext.NewWriter(stderr, access.Token)
	err = ops.RunWithEnv(ctx, dir, env, stdoutSafe, stderrSafe, "ci")

	return errors.Join(secrettext.RedactError(err, access.Token), stdoutSafe.Close(), stderrSafe.Close())
}
