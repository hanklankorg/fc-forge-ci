// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cienv

import (
	"fmt"
	"os"
)

// SetTemporarily sets name in the process environment and returns a func that
// restores the previous state. The environment is process-global, so callers
// use it only around a single-threaded step.
func SetTemporarily(name, value string) (func(), error) {
	old, had := os.LookupEnv(name)
	if err := os.Setenv(name, value); err != nil {
		return func() {}, fmt.Errorf("set %s: %w", name, err)
	}

	return func() {
		if had {
			_ = os.Setenv(name, old)

			return
		}

		_ = os.Unsetenv(name)
	}, nil
}
