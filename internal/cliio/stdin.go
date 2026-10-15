// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package cliio

import (
	"fmt"
	"io/fs"
	"os"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// StdinInput is one path-valued input of a command line, named by its flag.
type StdinInput struct{ Flag, Path string }

// SingleStdin refuses a command line on which more than one input reads
// stdin: the first reader would consume what the second expects, and the
// second would read nothing. It runs before either input is read.
func SingleStdin(inputs ...StdinInput) error {
	var first string

	for _, input := range inputs {
		if input.Path != StdSentinel {
			continue
		}

		if first != "" {
			return fmt.Errorf("--%s and --%s both read stdin (%q); only one input can: %w", first, input.Flag, StdSentinel, errs.ErrUsage)
		}

		first = input.Flag
	}

	return nil
}

func readStdin(stdin fs.File) ([]byte, error) {
	info, err := stdin.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat stdin: %w", err)
	}

	if info.Mode()&os.ModeCharDevice != 0 {
		return nil, fmt.Errorf("%q expects piped or redirected input, not a terminal: %w", StdSentinel, errs.ErrUsage)
	}

	body, err := readBounded(stdin)
	if err != nil {
		return nil, fmt.Errorf("read from stdin: %w", err)
	}

	return body, nil
}
