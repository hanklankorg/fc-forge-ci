// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Command credential-proxy runs the loopback CONNECT proxy inside a probe job,
// so the product and the tools it runs there reach only Forge Lab hosts.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

func main() {
	readyFile := flag.String("ready-file", "", "private file to create with the loopback proxy URL")

	flag.Parse()

	if flag.NArg() != 0 || *readyFile == "" ||
		!filepath.IsAbs(*readyFile) || filepath.Clean(*readyFile) != *readyFile {
		fmt.Fprintln(os.Stderr, "usage: credential-proxy --ready-file <absolute-new-file>")
		os.Exit(2)
	}

	proxy, err := livetest.StartCredentialProxy()
	if err != nil {
		fmt.Fprintln(os.Stderr, "credential proxy could not listen")
		os.Exit(2)
	}

	if err = writeReadyFile(*readyFile, proxy.URL()); err != nil {
		_ = proxy.Close()

		fmt.Fprintln(os.Stderr, "credential proxy could not publish readiness")
		os.Exit(2)
	}

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	select {
	case <-signalContext.Done():
		stop()
	case serveErr := <-proxy.Done():
		stop()

		if serveErr != nil {
			fmt.Fprintln(os.Stderr, "credential proxy stopped unexpectedly")
			os.Exit(1)
		}

		return
	}

	_ = proxy.Close()
}

func writeReadyFile(path, proxyURL string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // Caller supplies an absolute private job path.
	if err != nil {
		return err
	}

	if _, err = fmt.Fprintln(file, proxyURL); err != nil {
		_ = file.Close()

		return err
	}

	return file.Close()
}
