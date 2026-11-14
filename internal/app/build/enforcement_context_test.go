// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package build_test

import (
	"context"

	appenforcement "github.com/diggsweden/forge-ci/internal/app/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
)

// Native forwarding tests deliberately use the strict path. The report-only
// path tees test output (and uses Go JSON); its classification has separate tests.
func enforcingTests(ctx context.Context) context.Context {
	return appenforcement.WithAdmin(ctx, enforcement.Admin{enforcement.ApplicationTests: "true"})
}

func enforcingLint(ctx context.Context) context.Context {
	return appenforcement.WithAdmin(ctx, enforcement.Admin{enforcement.Lint: "true"})
}
