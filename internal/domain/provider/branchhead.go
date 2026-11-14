// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package provider

import "context"

// BranchHeadResolver reads a source branch's current commit. The explicit
// server is a confinement expectation, not an alternate credential destination.
type BranchHeadResolver interface {
	BranchHead(ctx context.Context, serverURL, repository, branch string) (string, error)
}
