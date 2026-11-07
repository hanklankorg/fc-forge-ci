// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
)

// Summary says in one line which findings the policy blocks and which it only
// reports. With the embedded default nothing blocks, which a person setting up
// CI does not expect: a failing test leaves the pull request green. The setup
// reports print this so that is not discovered after a merge.
func Summary(policy enforcement.Policy) string {
	var blocks, reports []string

	for _, check := range enforcement.Checks {
		name := string(check)
		if policy.Source(check) == enforcement.Flag {
			name += " (flag)"
		}

		if enforced, err := policy.Enforces(check); err == nil && enforced {
			blocks = append(blocks, name)
		} else {
			reports = append(reports, name)
		}
	}

	line := "blocks " + listOr(blocks, "nothing")
	if len(reports) > 0 {
		line += "; reports without blocking " + strings.Join(reports, ", ")
	}

	return line + ". In CI the workflow's administrator settings decide; see forge-ci docs admin-policy"
}

func listOr(items []string, none string) string {
	if len(items) == 0 {
		return none
	}

	return strings.Join(items, ", ")
}

// ShowText is Show for a person at a terminal: each setting, whether it
// blocks, and where it came from. Show's JSON stays the receipt CI logs.
func ShowText(ctx context.Context, out io.Writer) error {
	policy, err := Resolve(ctx)
	if err != nil {
		return err
	}

	for _, check := range enforcement.Checks {
		enforced, err := policy.Enforces(check)
		if err != nil {
			return err
		}

		effect := "reports, does not block"
		if enforced {
			effect = "blocks"
		}

		_, _ = fmt.Fprintf(out, "%-28s %-24s (%s)\n", enforcement.Setting(check), effect, policy.Source(check))
	}

	_, _ = fmt.Fprintf(out, "\nEmbedded default sha256: %s\nSet one with forge-ci --%s=true …; see forge-ci docs admin-policy\n",
		enforcement.Digest(), enforcement.Setting(enforcement.Lint))

	return nil
}
