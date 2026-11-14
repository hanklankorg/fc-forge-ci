// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package platform

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/adapters/github"
	apprunner "github.com/diggsweden/forge-ci/internal/app/runner"
	"github.com/diggsweden/forge-ci/internal/cli/clitoken"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/lender"
	"github.com/diggsweden/forge-ci/internal/runcontext"
)

// The lenders a request can name. "none", or no lender at all, is the
// installation whose container runners exist already; the request job then
// only says so.
const (
	lenderNone   = "none"
	lenderGitHub = "github"

	flagLender       = "lender"
	flagLenderTarget = "lender-repository"
	flagLenderRef    = "lender-ref"
	flagLenderAPIURL = "lender-api-url"
	flagArch         = "arch"
	flagTTLMinutes   = "ttl-minutes"

	defaultTTLMinutes = 20
)

// lenderSelection is what the flags say about the lender, before a token is
// read: enough to build the adapter.
type lenderSelection struct {
	Provider, Target, Ref, APIURL string
}

func requestRunnerCmd() *cli.Command {
	return requestRunnerCommand(newLender)
}

// Keep the command action testable without a GitHub server: the lender
// constructor is injected.
func requestRunnerCommand(build func(lenderSelection, runcontext.Credential) (lender.Lender, string, error)) *cli.Command {
	return &cli.Command{
		Name:  "request-runner",
		Usage: "ask a lender for one ephemeral runner before a job that builds container images",
		Description: "A runner pool that shares its kernel between tenants denies user namespaces, which rootless " +
			"Buildah and Podman need. The first job of such a workflow runs this command on the pool; a lender " +
			"starts one runner on a machine that permits them, registered to this forge for exactly one job with " +
			"the labels of CI_RUNNER_LABEL_CONTAINER (or its _ARM64 variant), and the container job waits on " +
			"those labels. With --lender none, or no lender configured, nothing is requested: the container runners exist already.\n\n" +
			"The lender's token is read from a file or stdin, never from the runner's own credentials: " +
			"those stay with the forge that issued them.\n\n" +
			"EXAMPLES:\n" +
			"   # Ask the GitHub repository example-org/runner-lender for an arm64 runner\n" +
			"   printf '%s' \"$CI_RUNNER_LENDER_TOKEN\" | forge-ci platform request-runner --lender github --lender-repository example-org/runner-lender --arch arm64 --token-file -\n\n" +
			"   # An installation with its own container runners\n" +
			"   forge-ci platform request-runner --lender none --arch amd64",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: flagLender, Sources: cli.EnvVars("CI_RUNNER_LENDER"), Usage: "github, or none; unset means none: the container runners exist already"},
			&cli.StringFlag{Name: flagLenderTarget, Sources: cli.EnvVars("CI_RUNNER_LENDER_REPOSITORY"), Usage: "github: owner/repository holding lend-runner.yml"},
			&cli.StringFlag{Name: flagLenderRef, Value: "main", Sources: cli.EnvVars("CI_RUNNER_LENDER_REF"), Usage: "github: the branch lend-runner.yml is dispatched on"},
			&cli.StringFlag{Name: flagLenderAPIURL, Sources: cli.EnvVars("CI_RUNNER_LENDER_API_URL"), Usage: "github: the API root of an enterprise server (default: api.github.com; never the runner's $GITHUB_API_URL)"},
			&cli.StringFlag{Name: flagArch, Required: true, Usage: "architecture of the container job: amd64 or arm64"},
			&cli.IntFlag{Name: flagTTLMinutes, Value: defaultTTLMinutes, Usage: "how long the lent runner waits for its job before the machine is released"},
			clitoken.FileFlag("The lender's token."),
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			selection := lenderSelection{
				Provider: strings.ToLower(strings.TrimSpace(cmd.String(flagLender))),
				Target:   strings.TrimSpace(cmd.String(flagLenderTarget)),
				Ref:      strings.TrimSpace(cmd.String(flagLenderRef)),
				APIURL:   cmd.String(flagLenderAPIURL),
			}

			in := apprunner.RequestInput{Arch: cmd.String(flagArch), TTLMinutes: cmd.Int(flagTTLMinutes)}

			if selection.Provider == "" || selection.Provider == lenderNone {
				return apprunner.Request(ctx, nil, os.Stdout, in)
			}

			token, err := clitoken.Resolve(cmd)
			if err != nil {
				return err
			}

			lend, name, err := build(selection, token)
			if err != nil {
				return err
			}

			in.LenderName = name

			return apprunner.Request(ctx, lend, os.Stdout, in)
		},
	}
}

// newLender turns the flags into the adapter for the named lender.
func newLender(selection lenderSelection, token runcontext.Credential) (lender.Lender, string, error) {
	switch selection.Provider {
	case lenderGitHub:
		owner, repository, ok := strings.Cut(selection.Target, "/")
		if !ok || owner == "" || repository == "" || strings.Contains(repository, "/") {
			return nil, "", fmt.Errorf("--%s must be owner/repository for the github lender, got %q: %w", flagLenderTarget, selection.Target, errs.ErrUsage)
		}

		if selection.Ref == "" {
			return nil, "", fmt.Errorf("--%s must name the branch lend-runner.yml is dispatched on: %w", flagLenderRef, errs.ErrUsage)
		}

		lend := &github.Lender{Owner: owner, Repository: repository, Ref: selection.Ref, APIBase: selection.APIURL, Token: token}

		return lend, lend.Name(), nil
	default:
		return nil, "", fmt.Errorf("--%s must be %s or %s, got %q: %w", flagLender, lenderGitHub, lenderNone, selection.Provider, errs.ErrUsage)
	}
}
