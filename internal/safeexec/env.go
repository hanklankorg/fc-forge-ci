// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package safeexec

import (
	"slices"
	"strings"
)

// EnvKeep returns the entries of env whose variable name keep accepts. An
// entry without "=" names no variable and is dropped.
func EnvKeep(env []string, keep func(name string) bool) []string {
	out := make([]string, 0, len(env))

	for _, entry := range env {
		if name, _, ok := strings.Cut(entry, "="); ok && keep(name) {
			out = append(out, entry)
		}
	}

	return out
}

// EnvWithout returns env without the named variables.
func EnvWithout(env []string, names ...string) []string {
	return EnvKeep(env, func(name string) bool { return !slices.Contains(names, name) })
}
