// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release_test

import (
	"os"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/evidence"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

// The dist-digest cross-check replays the reusable workflow's shell pipeline
// and is the only evidence that DistDigest still agrees with it byte for byte.
// It skips when the coreutils tools are absent, and without evidence.Main that
// skip would leave no trace: the tally would never be rendered and the package
// would report "ok" either way.
//
// No build tag: this package's tests are untagged, so the accounting has to be
// wired up for the default tier as well as the integration one.
//
// isolatedgit.Main isolates the whole process first, so the git the use cases
// run, which inherits os.Environ(), cannot read the developer's configuration.
func TestMain(m *testing.M) {
	os.Exit(isolatedgit.Main(func() int { return evidence.Main(m) }))
}
