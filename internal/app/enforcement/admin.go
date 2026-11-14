// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"context"
	"maps"
	"os"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
)

type adminKey struct{}

// WithAdmin carries the administrator's command-line settings to the gates of
// this invocation. Only the CLI's flag parsing calls it; nothing reads the
// settings from the environment, a file or the checkout.
func WithAdmin(ctx context.Context, admin enforcement.Admin) context.Context {
	if len(admin) == 0 {
		return ctx
	}

	return context.WithValue(ctx, adminKey{}, maps.Clone(admin))
}

func adminFrom(ctx context.Context) enforcement.Admin {
	admin, _ := ctx.Value(adminKey{}).(enforcement.Admin)

	return admin
}

// Resolve is the policy every gate applies: the embedded default with the
// administrator's settings of this invocation on top. An environment that
// carries a variable named like a setting is refused before anything runs.
func Resolve(ctx context.Context) (enforcement.Policy, error) {
	if err := enforcement.RefuseEnvironment(os.Environ()); err != nil {
		return enforcement.Policy{}, err
	}

	policy, err := enforcement.Current()
	if err != nil {
		return enforcement.Policy{}, err
	}

	return policy.WithAdmin(adminFrom(ctx))
}

// AdminArgs renders this invocation's administrator settings as the engine
// flags that set them, for a command that runs the engine again: a child
// gets the same policy as its parent, never a weaker default.
func AdminArgs(ctx context.Context) []string {
	admin := adminFrom(ctx)
	args := make([]string, 0, len(admin))

	for _, check := range enforcement.Checks {
		if value, set := admin[check]; set {
			args = append(args, "--"+enforcement.Setting(check)+"="+value)
		}
	}

	return args
}
