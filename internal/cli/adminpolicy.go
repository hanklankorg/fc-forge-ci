// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli

import (
	"context"

	"github.com/urfave/cli/v3"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
)

// adminPolicyFlags are the four enforcement settings. Their only source is
// the command line that runs the engine: they deliberately have no Sources,
// so no environment variable, plan file or project setting can supply one,
// and each may be given once. They are administrator policy only where the
// administrator writes that command line; the receipt therefore names the
// mechanism, flag, not an owner. Unset, a setting keeps the default
// embedded in this engine.
func adminPolicyFlags() []cli.Flag {
	flags := make([]cli.Flag, 0, len(enforcement.Checks))

	for _, check := range enforcement.Checks {
		setting := enforcement.Setting(check)
		flags = append(flags, &cli.StringFlag{
			Name:     setting,
			Category: "Administrator policy",
			Usage: "enforcement setting, true or false (default: the engine's embedded policy); false reports " + string(check) +
				" findings without blocking. Read only from the command line, never from the environment; the receipt names its source as flag",
			OnlyOnce: true,
			Validator: func(raw string) error {
				_, err := enforcement.ParseAdminValue(setting, raw)

				return err
			},
		})
	}

	return flags
}

// withAdminPolicy hands the administrator settings given on this command
// line to the gates. Values were admitted by the flags' validators.
func withAdminPolicy(ctx context.Context, cmd *cli.Command) context.Context {
	admin := enforcement.Admin{}

	for _, check := range enforcement.Checks {
		if setting := enforcement.Setting(check); cmd.IsSet(setting) {
			admin[check] = cmd.String(setting)
		}
	}

	return appenforcement.WithAdmin(ctx, admin)
}
