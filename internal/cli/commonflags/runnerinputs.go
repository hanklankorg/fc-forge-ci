// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package commonflags

import (
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
)

// FlagRunnerInput is the repeatable runner input a planning command admits
// its platform-bound jobs against. Its commands set DisableSliceFlagSeparator:
// a value is a JSON array, whose commas are not separators.
const FlagRunnerInput = "runner-input"

// RunnerInput is the shared "--runner-input name=value" flag.
func RunnerInput() *cli.StringSliceFlag {
	return &cli.StringSliceFlag{
		Name: FlagRunnerInput,
		Usage: "runner input of the calling layer as it passed it, name=value, such as build-runs-on-linux-arm64=[\"arm64\"] (repeatable); " +
			"a planned platform whose input has no value is refused; none passed checks nothing",
	}
}

// RequiredRunnerInput is RunnerInput for a plan a forge's layer never
// makes unchecked; read it with RequiredRunnerInputs.
func RequiredRunnerInput() *cli.StringSliceFlag {
	return &cli.StringSliceFlag{
		Name: FlagRunnerInput,
		Usage: "runner input of the calling layer as it passed it, name=value, such as build-runs-on-linux-arm64=[\"arm64\"] (repeatable, at least one on a forge); " +
			"a planned platform whose input has no value is refused",
	}
}

// RunnerInputs is the parsed --runner-input values of a layer of forge.
func RunnerInputs(cmd *cli.Command, forge provider.ForgeAPI) (*runners.Inputs, error) {
	return runners.ParseInputs(forge, cmd.StringSlice(FlagRunnerInput))
}

// RequiredRunnerInputs is RunnerInputs, refusing a forge's layer that
// passes none: plan names what is planned, as in "a release plan". A
// local run has no runners, so it takes no runner input and plans
// unchecked.
func RequiredRunnerInputs(cmd *cli.Command, forge provider.ForgeAPI, plan string) (*runners.Inputs, error) {
	inputs, err := RunnerInputs(cmd, forge)
	if err == nil && inputs == nil && forge.RunnerKeyword() != "" {
		err = fmt.Errorf("%s needs the layer's runner inputs: pass --%s for each: %w", plan, FlagRunnerInput, errs.ErrUsage)
	}

	return inputs, err
}
