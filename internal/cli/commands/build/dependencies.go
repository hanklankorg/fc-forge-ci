// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/diggsweden/forge-ci/internal/cli/secret"
	"github.com/diggsweden/forge-ci/internal/cli/secretenv"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/safeexec"
	"github.com/diggsweden/forge-ci/internal/secrettext"
	"github.com/urfave/cli/v3"
)

func dependencyFlags() []cli.Flag {
	return append([]cli.Flag{
		&cli.BoolFlag{Name: "validate-dependencies-only", Usage: "check dependency credentials/settings without running any project tool"},
	}, dependencyAccessFlags()...)
}

func dependencyAccessFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "private-dependencies", Sources: cli.EnvVars("PRIVATE_DEPENDENCIES"), Usage: "explicitly allow dependency resolution to use the read-only dependency credential"},
		&cli.StringFlag{Name: "dependency-token-file", Usage: "read-only package token file; defaults to $DEPENDENCY_TOKEN"},
		&cli.StringFlag{Name: "dependency-username", Sources: cli.EnvVars("DEPENDENCY_USERNAME"), Usage: "username for the project's dependency repository settings"},
	}
}

type dependencyEnvironment struct {
	base, authenticated []string
	token               string
}

func buildEnvironment() []string {
	return safeexec.EnvWithout(os.Environ(), secretenv.Names("DEPENDENCY_TOKEN", "NODE_AUTH_TOKEN",
		"RELEASE_GPG_PRIVATE_KEY", "RELEASE_GPG_PASSPHRASE", "RELEASE_COSIGN_PRIVATE_KEY", "RELEASE_COSIGN_PASSPHRASE", "MAVEN_CENTRAL_PASSWORD", "NPM_TOKEN")...)
}

func resolveDependencyEnvironment(cmd *cli.Command) (dependencyEnvironment, error) {
	base := buildEnvironment()
	result := dependencyEnvironment{base: base, authenticated: base}

	if !cmd.Bool("private-dependencies") {
		if cmd.String("dependency-token-file") != "" {
			return result, fmt.Errorf("--dependency-token-file requires --private-dependencies: %w", errs.ErrUsage)
		}

		return result, nil
	}

	token, err := secret.Resolve(cmd.String("dependency-token-file"), "DEPENDENCY_TOKEN")
	if err != nil {
		return result, err
	}

	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return result, fmt.Errorf("private dependencies need a nonempty single-line DEPENDENCY_TOKEN or --dependency-token-file: %w", errs.ErrPermissionDenied)
	}

	result.token = token
	result.authenticated = append(append([]string{}, base...), "DEPENDENCY_TOKEN="+token, "DEPENDENCY_USERNAME="+cmd.String("dependency-username"))

	return result, nil
}

func dependencyDiagnostics(token string, run func(io.Writer, io.Writer) error) error {
	if token == "" {
		return run(os.Stderr, os.Stderr)
	}

	stdout, stderr := secrettext.NewWriter(os.Stderr, token), secrettext.NewWriter(os.Stderr, token)
	err := run(stdout, stderr)

	return errors.Join(secrettext.RedactError(err, token), stdout.Close(), stderr.Close())
}
