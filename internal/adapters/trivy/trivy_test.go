// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package trivy_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/trivy"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

func TestAdapter_RunInheritPassesArgs(t *testing.T) {
	t.Parallel()

	mock := mockbinary.New(t)
	mock.Add("trivy", `printf 'trivy %s\n' "$*"`)
	a := &trivy.Adapter{Bin: mock.Path("trivy")}

	var stdout bytes.Buffer

	code, err := a.RunInherit(context.Background(), &stdout, &bytes.Buffer{}, "image", "alpine")
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}

	if !strings.Contains(stdout.String(), "trivy image alpine") {
		t.Errorf("stdout = %q", stdout.String())
	}

	want := []string{"image", "alpine", "--config="}
	if got := mock.Invocations("trivy")[0].Args; !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

// The gate's settings are the engine's flags, the platform among them.
// Forge-injected TRIVY_* variables and configuration in the caller's directory (the checkout) must
// not reach trivy, and neither may GITHUB_TOKEN, which trivy sends to ghcr.io
// and which holds the forge token on a Forgejo job.
func TestAdapter_RunInheritIsolatesEnvironmentAndDirectory(t *testing.T) {
	mock := mockbinary.New(t)
	mock.Add("trivy", `
for name in TRIVY_IGNORE_POLICY TRIVY_SKIP_DIRS TRIVY_DB_REPOSITORY TRIVY_CONFIG TRIVY_PLATFORM GITHUB_TOKEN UNRELATED; do
  if printenv "$name" >/dev/null; then echo "leaked $name"; exit 42; fi
done
[ "${HTTPS_PROXY:-}" = http://proxy.example:3128 ] || exit 44
[ -z "$(ls -A .)" ] || exit 45
`)

	checkout := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(checkout, "trivy.yaml"), []byte("vulnerability:\n  ignore-unfixed: true\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(checkout, ".trivyignore"), []byte("CVE-2026-0001\n"), 0o600))
	t.Chdir(checkout)
	t.Setenv("TRIVY_IGNORE_POLICY", "/tmp/allow-all.rego")
	t.Setenv("TRIVY_SKIP_DIRS", "/usr")
	t.Setenv("TRIVY_DB_REPOSITORY", "registry.example/empty-db")
	t.Setenv("TRIVY_CONFIG", "trivy.yaml")
	t.Setenv("GITHUB_TOKEN", "forge-job-token")
	t.Setenv("UNRELATED", "value")
	t.Setenv("TRIVY_PLATFORM", "linux/arm64")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")

	var stdout bytes.Buffer

	code, err := (&trivy.Adapter{Bin: mock.Path("trivy")}).RunInherit(context.Background(), &stdout, &bytes.Buffer{}, "image", "alpine")
	require.NoError(t, err)
	require.Zero(t, code, "trivy saw a planted setting: %s", stdout.String())

	invocation := mock.Invocations("trivy")[0]
	assert.Equal(t, []string{"image", "alpine", "--config="}, invocation.Args, "configuration loading must be disabled")
	assert.NotEqual(t, checkout, invocation.Cwd)
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
	if !errors.Is(err, errs.ErrPrerequisite) {
		t.Errorf("err = %v, want wrapped errs.ErrPrerequisite", err)
	}
}
