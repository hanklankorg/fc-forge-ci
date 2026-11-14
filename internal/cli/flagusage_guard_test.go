// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cli_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	internalcli "github.com/diggsweden/forge-ci/internal/cli"
)

// urfave/cli takes the first backticked word of a flag's Usage as the value
// placeholder in --help, so `--plan config parse-artifacts` would replace
// "string". Flag usages quote with double quotes instead.
func TestFlagUsageHasNoBacktick(t *testing.T) {
	t.Parallel()

	var walk func(cmd *cli.Command, path string)

	walk = func(cmd *cli.Command, path string) {
		for _, flag := range cmd.Flags {
			usage, ok := flag.(interface{ GetUsage() string })
			if ok && strings.Contains(usage.GetUsage(), "`") {
				t.Errorf("%s --%s: usage has a backtick, which --help turns into the value placeholder: %s",
					path, flag.Names()[0], usage.GetUsage())
			}
		}

		for _, sub := range cmd.Commands {
			walk(sub, path+" "+sub.Name)
		}
	}

	walk(internalcli.New(internalcli.BuildInfo{}), "forge-ci")
}

// A key=value is one value even when its data contains commas. Exercise each
// declared pair flag through the real parser while replacing only the action:
// no registry, build tool or provider is involved in this contract.
func TestKeyValueSliceFlagsPreserveCommas(t *testing.T) {
	t.Parallel()

	var walk func(cmd *cli.Command, path string)

	walk = func(cmd *cli.Command, path string) {
		for _, flag := range cmd.Flags {
			slice, ok := flag.(*cli.StringSliceFlag)
			if !ok || !strings.Contains(slice.Usage, "=") {
				continue
			}

			t.Run(path+"/"+slice.Name, func(t *testing.T) {
				var values []string

				probe := &cli.Command{
					Name: "pair", Flags: []cli.Flag{slice},
					DisableSliceFlagSeparator: cmd.DisableSliceFlagSeparator,
					Action: func(_ context.Context, parsed *cli.Command) error {
						values = parsed.StringSlice(slice.Name)

						return nil
					},
				}
				want := []string{"key=one,two", "other=three,four"}

				if err := probe.Run(t.Context(), []string{"pair", "--" + slice.Name, want[0], "--" + slice.Name, want[1]}); err != nil {
					t.Fatal(err)
				}

				if !reflect.DeepEqual(values, want) {
					t.Errorf("pair data split on commas: got %q, want %q; disable the command's slice separator", values, want)
				}
			})
		}

		for _, sub := range cmd.Commands {
			walk(sub, path+"/"+sub.Name)
		}
	}

	walk(internalcli.New(internalcli.BuildInfo{}), "forge-ci")
}
