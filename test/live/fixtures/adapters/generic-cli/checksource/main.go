// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Command checksource runs the generic Go CLI adapter's pristine-source gate.
// It is acceptance-fixture tooling, not a distributed Forge CI command.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := genericCLIChecks(ctx)

	cancel()

	if err != nil {
		fmt.Fprintf(os.Stderr, "generic CLI source checks: %v\n", err)
		os.Exit(1)
	}
}
