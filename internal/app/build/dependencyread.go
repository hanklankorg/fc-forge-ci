// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// retryDependencyRead runs a build tool's dependency download, which writes
// its errors to the stderr it is given. It is run again only when the tool
// states a transport failure, and then ends as the dependency being
// unavailable; a checksum the tool refuses, a status and every other failure
// return from the first attempt as the tool's own.
func retryDependencyRead(ctx context.Context, stderr io.Writer, attempts int, delay time.Duration, report func(string) string, read func(stderr io.Writer) error) error {
	return retry.Run(ctx, stderr, attempts, delay, func() error {
		if err := ctx.Err(); err != nil {
			return retry.Permanent(err)
		}

		var said bytes.Buffer

		heard := io.Writer(&said)
		if stderr != nil {
			heard = io.MultiWriter(stderr, &said)
		}

		err := read(heard)
		if err == nil {
			return nil
		}

		class := errs.ToolFailure(report(said.String()))
		if !errs.Retriable(class) {
			return retry.Permanent(err)
		}

		//nolint:errorlint // flattened: the tool's failure class must not outrank the retriable one.
		return fmt.Errorf("%v: %w", err, class)
	})
}
