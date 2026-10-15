// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package trivy_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/adapters/trivy"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

func TestAdapter_RunInheritPassesArgs(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("trivy", `printf 'trivy %s\n' "$*"`)
	a := &trivy.Adapter{Bin: m.Path("trivy")}

	var stdout bytes.Buffer

	code, err := a.RunInherit(context.Background(), &stdout, &bytes.Buffer{}, "image", "alpine")
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}

	if !strings.Contains(stdout.String(), "trivy image alpine") {
		t.Errorf("stdout = %q", stdout.String())
	}

	want := []string{"image", "alpine"}
	if got := m.Invocations("trivy")[0].Args; !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

// Trivy authenticates to ghcr.io with GITHUB_TOKEN, which on a Forgejo job is
// the forge token: the adapter must not hand it, or any other credential it
// is told to drop, to trivy.
func TestAdapter_RunInheritUnsetsCredentials(t *testing.T) {
	m := mockbinary.New(t)
	m.Add("trivy", `
if [ -n "${GITHUB_TOKEN:-}" ] || [ -n "${REGISTRY_TOKEN:-}" ]; then
  exit 42
fi
[ "${KEEP_ME:-}" = kept ] || exit 43
`)
	t.Setenv("GITHUB_TOKEN", "forge-job-token")
	t.Setenv("REGISTRY_TOKEN", "token")
	t.Setenv("KEEP_ME", "kept")

	a := trivy.New("GITHUB_TOKEN", "REGISTRY_TOKEN")
	a.Bin = m.Path("trivy")

	code, err := a.RunInherit(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, "image", "alpine")
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v; want the credentials removed and other variables kept", code, err)
	}
}

func TestAdapter_RunInheritReturnsExitCode(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("trivy", `exit 5`)
	a := &trivy.Adapter{Bin: m.Path("trivy")}

	code, err := a.RunInherit(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, "image", "alpine")
	if err != nil || code != 5 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestAdapter_RunInheritMissingBinaryClassifies(t *testing.T) {
	t.Parallel()

	// A non-exit failure (trivy absent from PATH) must classify as
	// ErrDependencyUnavailable (EX_UNAVAILABLE 69), not fall through to
	// the unclassified internal-bug default.
	a := &trivy.Adapter{Bin: "trivy-does-not-exist-xyz"}

	_, err := a.RunInherit(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, "image", "alpine")
	if !errors.Is(err, errs.ErrDependencyUnavailable) {
		t.Errorf("err = %v, want wrapped errs.ErrDependencyUnavailable", err)
	}
}
