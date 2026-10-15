// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package clitoken resolves a command's token input into a
// runcontext.Credential.
//
// It is the composition root's one credential-minting seam. It exists as its
// own package so the rule "only the composition root turns a string into an
// unrestricted credential" is a single, greppable call site rather than a
// convention repeated in every command.
package clitoken

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/cli/secret"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

const tokenFileFlag = "token-file"

// FileFlag declares explicit token input without putting the secret in argv.
// No env Sources: ambient credentials must retain their issuing-server scope.
func FileFlag(purpose string) *cli.StringFlag {
	return &cli.StringFlag{
		Name: tokenFileFlag,
		Usage: purpose + " Read from a file (\"-\" for stdin); trailing CR/LF is trimmed. " +
			"Explicit input overrides environment credentials; empty content supplies no token. " +
			"Omit for environment selection, with runner-issued tokens scoped to their server.",
	}
}

// Resolve returns the credential a command should use.
//
// An explicit file/stdin token wins and is unrestricted: the operator selected
// the credential for this command. An empty file deliberately supplies no token;
// neither empty content nor a read failure falls back to an ambient credential.
//
// Otherwise the runner's own token is used, bound to the server that issued
// it. A plain-string env resolver would discard that scope and could send a
// runner token to a third-party forge.
func Resolve(cmd *cli.Command) (runcontext.Credential, error) {
	if cmd.IsSet(tokenFileFlag) {
		return fileCredential(cmd.String(tokenFileFlag))
	}

	return runcontext.Token().Resolve(os.Getenv), nil
}

// ResolveRelease is Resolve for release operations. Its chain prefers a
// dedicated $RELEASE_TOKEN before the forge's ambient one.
func ResolveRelease(cmd *cli.Command) (runcontext.Credential, error) {
	if cmd.IsSet(tokenFileFlag) {
		return fileCredential(cmd.String(tokenFileFlag))
	}

	return runcontext.ReleaseToken().Resolve(os.Getenv), nil
}

func fileCredential(path string) (runcontext.Credential, error) {
	if path == "" {
		return runcontext.Credential{}, fmt.Errorf("--token-file requires a file path or '-' for stdin: %w", errs.ErrUsage)
	}

	value, err := secret.Resolve(path, "")
	if err != nil {
		return runcontext.Credential{}, err
	}

	return runcontext.OperatorCredential(value), nil
}
