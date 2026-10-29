// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package validate

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	appvalidate "github.com/diggsweden/forge-ci/internal/app/validate"
	"github.com/diggsweden/forge-ci/internal/cli/cienv"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/listval"
)

// Only providers with authenticated reusable-run metadata implement this role.
// A bare workflow_call value never becomes an allowed event on its own.
type workflowCallEventResolver interface {
	ResolveWorkflowCallEvent(context.Context) (string, error)
}

func eventContextCmd() *cli.Command {
	return &cli.Command{
		Name:  "event-context",
		Usage: "refuse to run when the workflow trigger is outside the publish/release allowlist",
		Description: "Defense-in-depth guard placed at the entry of every privileged " +
			"publish/release workflow. Reads the trigger event and refuses any trigger " +
			"outside the allowlist — most importantly the pull_request* family, which " +
			"would otherwise run with the caller's signing/package/API secrets attached " +
			"to PR-HEAD code. Default allowlist: push, workflow_dispatch, release, " +
			"schedule, workflow_run, merge_group. Adopters with legitimate PR-context " +
			"publish needs (preview deploys) override via --allowed-events on the step. " +
			"Forgejo reusable calls resolve the original trigger from authenticated current-run " +
			"metadata bound to the runner repository, run ID and source SHA; missing or ambiguous " +
			"metadata is refused. An event-name override cannot relabel those reusable calls.\n\n" +
			"EXAMPLE:\n" +
			"   # Reads $FORGEJO_EVENT_NAME / $GITHUB_EVENT_NAME, or the detected provider's\n" +
			"   # event context (GitLab: normalized CI_PIPELINE_SOURCE); refuses pull_request*\n" +
			"   # and other non-allowlisted triggers\n" +
			"   forge-ci validate event-context",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "event-name",
				Sources: cienv.EventName(),
				Usage: "trigger event being checked; read from $FORGEJO_EVENT_NAME / " +
					"$GITHUB_EVENT_NAME, or from the detected CI provider's event context " +
					"(GitLab: normalized CI_PIPELINE_SOURCE) when unset",
			},
			&cli.StringFlag{
				Name:    "allowed-events",
				Sources: cli.EnvVars("ALLOWED_EVENTS"),
				Usage:   "comma/space/newline-separated allowlist override (default: push,workflow_dispatch,release,schedule,workflow_run,merge_group)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return deps.FromCmd(ctx, cmd, func(dependencies *deps.Deps) error {
				eventName := cmd.String("event-name")
				if eventName == "" {
					// No env var and no flag (GitLab runners set neither
					// GITHUB_* nor FORGEJO_*): fall back to the provider's
					// resolved event context, which carries the canonical
					// vocabulary. An unresolvable event stays empty and the
					// gate fails closed on missing input.
					evt, err := dependencies.Provider.ResolveContext(ctx)
					if err != nil {
						return err
					}

					eventName = evt.EventName
				}
				if resolver, ok := dependencies.Provider.(workflowCallEventResolver); ok {
					native, err := dependencies.Provider.ResolveContext(ctx)
					if err != nil {
						return err
					}
					// An event-name argument cannot relabel a native reusable run.
					if eventName == "workflow_call" || native.EventName == "workflow_call" {
						resolved, err := resolver.ResolveWorkflowCallEvent(ctx)
						if err != nil {
							return err
						}
						eventName = resolved
					}
				}

				return appvalidate.EventContext(os.Stderr, deps.Annotator(cmd), appvalidate.EventContextInput{
					EventName:     eventName,
					AllowedEvents: splitAllowedEvents(cmd.String("allowed-events")),
				})
			})
		},
	}
}

// splitAllowedEvents tolerates the same separators the workflow caller
// might naturally use — comma, space, newline, tab. Returns nil for
// empty input so the app layer falls back to DefaultAllowedEvents.
func splitAllowedEvents(raw string) []string {
	return listval.Tokens(raw)
}
