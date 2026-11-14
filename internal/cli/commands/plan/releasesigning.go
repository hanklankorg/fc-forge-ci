// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan

import (
	"context"
	"strings"

	"github.com/urfave/cli/v3"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/cli/deps"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	domainsummary "github.com/diggsweden/forge-ci/internal/domain/summary"
)

// warnReleaseSigning warns, in the log and in the job summary, when the
// forge's release signs the declaration's files with another method or key
// than it declares, or decides their transparency by its own input. The plan
// goes on: one declaration often serves several forges.
func warnReleaseSigning(ctx context.Context, cmd *cli.Command, plans appconfig.ReleaseStagePlans, forge provider.ForgeAPI) error {
	warnings := pipeline.ReleaseSigningWarnings(plans.Sign, forge)
	if len(warnings) == 0 {
		return nil
	}

	// The declaration is the project's to edit, so each warning, which quotes
	// it, is rendered as literal text.
	var summary strings.Builder

	summary.WriteString("## Release signing\n")

	for _, warning := range warnings {
		deps.Annotator(cmd).Warningf("%s", warning)

		summary.WriteString("\n" + domainsummary.LiteralText(warning) + "\n")
	}

	return deps.FromCmd(ctx, cmd, func(d *deps.Deps) error {
		return d.SummarySink.Append(ctx, summary.String())
	})
}
