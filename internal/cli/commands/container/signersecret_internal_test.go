// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/syft"
	"github.com/diggsweden/forge-ci/internal/runcontext"
	"github.com/diggsweden/forge-ci/internal/secretenv"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

// The syft at the signer boundary scans while the run holds signing and
// registry credentials. It runs with an allowlisted environment, so none of
// them reaches it: not the names runcontext resolves as credentials, not the
// shared scrub list, and not the variable an env:// signing key names, which
// no fixed list could hold. Registry configuration it needs to pull stays.
func TestSignerSyft_RunsWithoutAnyCredential(t *testing.T) {
	credentials := slices.Concat(runcontext.Token().Keys(), runcontext.ReleaseToken().Keys(), secretenv.Names("MY_SIGNING_KEY"))
	if len(credentials) == 0 {
		t.Fatal("no credential names; the accessors, not the environment, are what was measured")
	}

	slices.Sort(credentials)
	credentials = slices.Compact(credentials)

	mock := mockbinary.New(t)
	mock.OnPath()
	mock.Add("syft", `
for name in `+strings.Join(credentials, " ")+`; do
  if printenv "$name" >/dev/null; then echo "syft was given $name" >&2; exit 4; fi
done
[ "${REGISTRY_AUTH_FILE:-}" = /run/auth.json ] || { echo 'REGISTRY_AUTH_FILE missing' >&2; exit 5; }
`)

	for _, name := range credentials {
		t.Setenv(name, "fixture-credential")
	}

	t.Setenv("REGISTRY_AUTH_FILE", "/run/auth.json")

	var stderr strings.Builder
	if err := syft.New().RunInherit(t.Context(), io.Discard, &stderr, "version"); err != nil {
		t.Fatalf("signer syft: %v: %s", err, stderr.String())
	}
}
