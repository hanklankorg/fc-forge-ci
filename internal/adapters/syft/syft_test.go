// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package syft_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/diggsweden/forge-ci/internal/adapters/syft"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/mockbinary"
)

// configArgs is the tail every invocation carries: the private empty
// configuration file that replaces the discovered ones.
func configArgs(t *testing.T, args []string) []string {
	t.Helper()

	require.GreaterOrEqual(t, len(args), 2)
	tail := args[len(args)-2:]
	require.Equal(t, "--config", tail[0])
	require.Equal(t, "syft.yaml", filepath.Base(tail[1]))

	return args[:len(args)-2]
}

// Relative output files and a relative path source resolve against the
// caller's directory, since syft runs in another one; format order is sorted.
func TestAdapter_GenerateUsesSortedAbsoluteArgs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "target"), 0o700))
	t.Chdir(dir)

	mock := mockbinary.New(t)
	mock.Add("syft", `printf 'ok\n' >&2`)
	a := &syft.Adapter{Bin: mock.Path("syft")}

	var stderr bytes.Buffer

	err := a.Generate(context.Background(), "./target", map[string]string{
		"spdx-json":      "out.spdx.json",
		"cyclonedx-json": "out.cyclonedx.json",
	}, &stderr)
	require.NoError(t, err)

	cwd, err := os.Getwd()
	require.NoError(t, err)

	want := []string{
		filepath.Join(cwd, "target"),
		"-o", "cyclonedx-json=" + filepath.Join(cwd, "out.cyclonedx.json"),
		"-o", "spdx-json=" + filepath.Join(cwd, "out.spdx.json"),
	}
	assert.Equal(t, want, configArgs(t, mock.Invocations("syft")[0].Args))
	assert.Equal(t, "ok", strings.TrimSpace(stderr.String()))
}

// A file scheme's path is made absolute; an image reference, with or without
// a scheme, is passed as given.
func TestAdapter_GenerateResolvesOnlyPathSources(t *testing.T) {
	t.Chdir(t.TempDir())

	cwd, err := os.Getwd()
	require.NoError(t, err)

	for target, want := range map[string]string{
		"oci-dir:layout":                    "oci-dir:" + filepath.Join(cwd, "layout"),
		"docker-archive:image.tar":          "docker-archive:" + filepath.Join(cwd, "image.tar"),
		"oci-dir:/abs/layout":               "oci-dir:/abs/layout",
		"registry:ghcr.io/example/app:1":    "registry:ghcr.io/example/app:1",
		"ghcr.io/example/app@sha256:abc123": "ghcr.io/example/app@sha256:abc123",
	} {
		mock := mockbinary.New(t)
		mock.Add("syft", `true`)

		require.NoError(t, (&syft.Adapter{Bin: mock.Path("syft")}).Generate(t.Context(), target,
			map[string]string{"cyclonedx-json": "/out/sbom.json"}, io.Discard))
		assert.Equal(t, want, configArgs(t, mock.Invocations("syft")[0].Args)[0], target)
	}
}

// The SBOM's contents are the engine's flags. Forge-injected SYFT_* variables
// and a .syft.yaml in the caller's directory (the checkout) could exclude
// paths or catalogers from an SBOM the engine signs; neither may reach syft,
// on either entry point.
func TestAdapter_IsolatesEnvironmentAndDirectory(t *testing.T) {
	mock := mockbinary.New(t)
	mock.Add("syft", `
for name in SYFT_EXCLUDE SYFT_CONFIG SYFT_CATALOGERS COSIGN_KEY GITHUB_TOKEN UNRELATED; do
  if printenv "$name" >/dev/null; then echo "leaked $name" >&2; exit 42; fi
done
[ "${HTTPS_PROXY:-}" = http://proxy.example:3128 ] || exit 43
[ "${DOCKER_CONFIG:-}" = /run/docker ] || exit 44
[ "$(ls -A)" = syft.yaml ] || { echo "found $(ls -A)" >&2; exit 45; }
[ "$(cat syft.yaml)" = '{}' ] || exit 46
printf 'ok\n' >&2
`)

	checkout := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(checkout, ".syft.yaml"), []byte("exclude:\n  - ./**\n"), 0o600))
	t.Chdir(checkout)
	t.Setenv("SYFT_EXCLUDE", "./vendor/**")
	t.Setenv("SYFT_CONFIG", ".syft.yaml")
	t.Setenv("SYFT_CATALOGERS", "none")
	t.Setenv("COSIGN_KEY", "secret")
	t.Setenv("GITHUB_TOKEN", "forge-job-token")
	t.Setenv("UNRELATED", "value")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	t.Setenv("DOCKER_CONFIG", "/run/docker")

	adapter := &syft.Adapter{Bin: mock.Path("syft")}

	for name, run := range map[string]func(stderr io.Writer) error{
		"Generate": func(stderr io.Writer) error {
			return adapter.Generate(t.Context(), "image@sha256:abc", map[string]string{"cyclonedx-json": "out.json"}, stderr)
		},
		"RunInherit": func(stderr io.Writer) error {
			return adapter.RunInherit(t.Context(), io.Discard, stderr, "--version")
		},
	} {
		var stderr bytes.Buffer

		err := run(&stderr)
		require.NoError(t, err, "%s: %s", name, stderr.String())
		assert.Equal(t, "ok", strings.TrimSpace(stderr.String()), name)
	}

	for _, invocation := range mock.Invocations("syft") {
		assert.NotEqual(t, checkout, invocation.Cwd)
	}
}

func TestAdapter_GenerateRequiresOutputs(t *testing.T) {
	a := &syft.Adapter{Bin: "syft"}

	err := a.Generate(context.Background(), "./target", nil, &bytes.Buffer{})
	if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), "no outputs requested") {
		t.Fatalf("err = %v, want ErrUsage naming the missing outputs", err)
	}
}

func TestAdapter_RunInherit(t *testing.T) {
	t.Parallel()

	m := mockbinary.New(t)
	m.Add("syft", `printf 'syft %s\n' "$*"`)
	a := &syft.Adapter{Bin: m.Path("syft")}

	var stdout bytes.Buffer
	if err := a.RunInherit(context.Background(), &stdout, &bytes.Buffer{}, "--version"); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(stdout.String(), "syft --version --config ") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// TestSyft_MissingBinaryIsADependencyProblem covers both entry points.
//
// syft is installed by the toolchain step, so "not on PATH" means that step
// did not run or did not finish -- an environment problem the operator can
// act on. Unclassified it exited 70, "file a bug against forge-ci".
func TestSyft_MissingBinaryIsADependencyProblem(t *testing.T) {
	adapter := &syft.Adapter{Bin: "syft-does-not-exist"}

	for name, run := range map[string]func() error{
		"Generate": func() error {
			return adapter.Generate(context.Background(), "img",
				map[string]string{"spdx-json": filepath.Join(t.TempDir(), "out.json")}, io.Discard)
		},
		"RunInherit": func() error {
			return adapter.RunInherit(context.Background(), io.Discard, io.Discard, "--version")
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if !errors.Is(err, errs.ErrPrerequisite) {
				t.Fatalf("err = %v, want ErrPrerequisite", err)
			}

			if !strings.Contains(err.Error(), "syft-does-not-exist") {
				t.Errorf("err = %v, want it to name the binary", err)
			}
		})
	}
}
